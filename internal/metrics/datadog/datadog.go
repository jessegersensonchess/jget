// Package datadog implements a Datadog backend for the internal/metrics package.
//
// NOTE ABOUT FLUSHING:
// This backend is meant to be useful for both short-lived and long-running ETL jobs.
// Submitting only once at process exit can make Datadog dashboards/monitors awkward
// for long jobs (you get a single spike rather than a time series).
//
// Therefore we:
//   - buffer metrics in-memory (fast, lock-protected)
//   - periodically Flush() on a ticker (default: once per minute)
//   - Flush() one final time on Close()
//
// This gives you:
//   - time series points while the job is running
//   - a final “tail” flush at shutdown
//
// Concurrency model:
//   - ETL goroutines can call IncCounter/SetGauge/ObserveHistogram/ObserveDistribution at any time
//   - Flush snapshots+resets buffers under a mutex, then submits out-of-lock
//   - The flush loop calls Flush() periodically; Close() stops the loop
//
// If the process is killed with SIGKILL/OOM, Close() won’t run (no backend can fix that).
//
// Design goals (intentionally opinionated):
//   - Keep the core ETL code depending only on metrics.Backend.
//   - Buffer metrics in-memory and submit them on Flush().
//   - Avoid shipping Prometheus-specific or Datadog-specific code into the core.
//
// IMPORTANT CHANGE (generic counters & gauges):
// Earlier versions of this backend whitelisted counter names and ignored unknown counters.
// That prevented tools from emitting new counter metrics (e.g. queue.tasks.created)
// without modifying the backend.
//
// This implementation makes counters and gauges *generic*:
//   - Any metric name passed to IncCounter/SetGauge will be buffered.
//   - On Flush(), counters are submitted as Datadog v2 COUNT series.
//   - On Flush(), gauges are submitted as Datadog v2 GAUGE series.
//
// Backwards compatibility for existing ETL dashboards is preserved by mapping a few
// legacy internal names (etl_step_total, etc.) to dotted Datadog metric names.
package datadog

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"etl/internal/metrics"

	dd "github.com/DataDog/datadog-api-client-go/v2/api/datadog"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// Options controls Datadog backend configuration.
type Options struct {
	// JobName becomes tag "job:<name>" on every metric.
	// If empty, defaults to "etl".
	JobName string

	// Tags are extra Datadog tags (e.g. []string{"env:prod", "service:etl"}).
	Tags []string

	// FlushEvery controls how often we submit buffered metrics to Datadog.
	// If <= 0, defaults to 60 seconds.
	FlushEvery time.Duration

	// The following fields are unexported test seams.
	//
	// They are intentionally kept private to preserve the public API surface.
	// Production code will never set them; unit tests can set them to avoid:
	//   - real network submission
	//   - nondeterministic clocks/tickers
	now       func() time.Time
	newTicker func(d time.Duration) *time.Ticker

	// Optional seam for v2 (count/gauge) submission.
	submitter metricsSubmitterV2

	// Optional seam for v1 distribution points submission.
	submitterDist metricsSubmitterV1
}

// metricsSubmitterV2 is the minimal interface needed to submit v2 metrics.
type metricsSubmitterV2 interface {
	SubmitMetrics(ctx context.Context, body datadogV2.MetricPayload, params ...datadogV2.SubmitMetricsOptionalParameters) (datadogV2.IntakePayloadAccepted, *http.Response, error)
}

// metricsSubmitterV1 is the minimal interface needed to submit v1 distribution points.
type metricsSubmitterV1 interface {
	SubmitDistributionPoints(ctx context.Context, body datadogV1.DistributionPointsPayload, params ...datadogV1.SubmitDistributionPointsOptionalParameters) (datadogV1.IntakePayloadAccepted, *http.Response, error)
}

// Backend implements metrics.Backend for Datadog.
type Backend struct {
	apiV2   metricsSubmitterV2
	apiDist metricsSubmitterV1
	ctx     context.Context

	flushEvery time.Duration
	stopCh     chan struct{}
	doneCh     chan struct{}

	// baseTags are attached to every metric at submission time.
	// They include env:<...>, job:<...>, plus user provided opts.Tags.
	baseTags []string

	// now is injected for deterministic tests. Production uses time.Now.
	now func() time.Time

	// newTicker is injected for deterministic tests. Production uses time.NewTicker.
	newTicker func(d time.Duration) *time.Ticker

	mu sync.Mutex

	// --- Generic v2 metrics (COUNT/GAUGE) ---
	//
	// We buffer counters and gauges by:
	//   metricName -> tagKey -> value
	//
	// tagKey is a stable string derived from labels (sorted "k:v" pairs).
	counters map[string]map[string]float64
	gauges   map[string]map[string]float64

	// --- Histogram-style samples (still intentionally opinionated) ---
	//
	// We keep histogram sampling behavior the same as before: only a known set of
	// histogram names are buffered, and on Flush() we submit fixed percentile gauges
	// (p50/p90/p95/p99/max/samples) as v2 GAUGE series.
	//
	// This is distinct from ObserveDistribution(), which sends native v1 distributions.
	durationSamples map[string][]float64 // key: step\x00status -> samples

	httpReqDur    map[string][]float64 // key: status -> samples
	httpRespDur   map[string][]float64 // key: status -> samples
	httpDownloadB map[string][]float64 // key: status -> samples

	// --- Native Datadog distributions (v1) ---
	// metric -> tagKey -> samples
	distributions map[string]map[string][]float64
}

func resolveEnvTag() string {
	if v := strings.TrimSpace(os.Getenv("ENV")); v != "" {
		return "env:" + v
	}
	if v := strings.TrimSpace(os.Getenv("DD_ENV")); v != "" {
		return "env:" + v
	}
	return "env:unknown"
}

// resolveAPIHost returns an absolute API base URL.
// Preference order:
//  1. DATADOG_HOST (recommended by Datadog for EU orgs)
//  2. DD_SITE -> https://api.<site>
//  3. empty (SDK default)
func resolveAPIHost() string {
	if v := strings.TrimSpace(os.Getenv("DATADOG_HOST")); v != "" {
		return v
	}
	if site := strings.TrimSpace(os.Getenv("DD_SITE")); site != "" {
		return "https://api." + site
	}
	return ""
}

func (b *Backend) loop() {
	defer close(b.doneCh)

	t := b.newTicker(b.flushEvery)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			_ = b.Flush()
		case <-b.stopCh:
			return
		}
	}
}

// Close stops the background flush loop and performs one final Flush().
func (b *Backend) Close() error {
	close(b.stopCh)
	<-b.doneCh
	return b.Flush()
}

// NewBackend constructs a Datadog backend using the official client.
//
// This backend sends:
//   - v2 SubmitMetrics for COUNT/GAUGE series (generic counters & gauges + percentile gauges)
//   - v1 SubmitDistributionPoints for native distribution samples (ObserveDistribution)
func NewBackend(parent context.Context, opts Options) (*Backend, error) {
	job := opts.JobName
	if job == "" {
		job = "etl"
	}

	flushEvery := opts.FlushEvery
	if flushEvery <= 0 {
		flushEvery = 60 * time.Second
	}

	envTag := resolveEnvTag()
	baseTags := make([]string, 0, 2+len(opts.Tags))
	baseTags = append(baseTags, envTag, "job:"+job)
	baseTags = append(baseTags, opts.Tags...)

	// Clock / ticker seams.
	nowFn := opts.now
	if nowFn == nil {
		nowFn = time.Now
	}
	newTicker := opts.newTicker
	if newTicker == nil {
		newTicker = time.NewTicker
	}

	// Submitter seams.
	submitterV2 := opts.submitter
	submitterDist := opts.submitterDist

	if submitterV2 == nil || submitterDist == nil {
		cfg := dd.NewConfiguration()

		if host := resolveAPIHost(); host != "" {
			cfg.Servers = dd.ServerConfigurations{{URL: host}}
		}

		client := dd.NewAPIClient(cfg)

		if submitterV2 == nil {
			submitterV2 = datadogV2.NewMetricsApi(client)
		}
		if submitterDist == nil {
			submitterDist = datadogV1.NewMetricsApi(client)
		}
	}

	ctx := dd.NewDefaultContext(parent)

	b := &Backend{
		apiV2:   submitterV2,
		apiDist: submitterDist,
		ctx:     ctx,

		flushEvery: flushEvery,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),

		baseTags: baseTags,

		now:       nowFn,
		newTicker: newTicker,

		// Generic buffers:
		counters: make(map[string]map[string]float64),
		gauges:   make(map[string]map[string]float64),

		// Histogram buffers:
		durationSamples: make(map[string][]float64),
		httpReqDur:      make(map[string][]float64),
		httpRespDur:     make(map[string][]float64),
		httpDownloadB:   make(map[string][]float64),

		// Distribution buffers:
		distributions: make(map[string]map[string][]float64),
	}

	go b.loop()
	return b, nil
}

// IncCounter implements metrics.Backend (generic).
//
// Any metric name is accepted and buffered.
// On Flush(), counters are submitted as Datadog v2 COUNT series.
//
// Notes:
//   - delta <= 0 is ignored.
//   - Labels are converted to stable tags (sorted).
func (b *Backend) IncCounter(name string, delta float64, labels metrics.Labels) {
	if delta <= 0 {
		return
	}

	// Normalize HTTP status tag.
	if name == "etl_http_requests_total" || name == "etl_http_errors_total" {
		if labels == nil {
			labels = metrics.Labels{}
		}
		if labels["status"] == "" {
			labels["status"] = "unknown"
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	tagKey := labelsKey(labels)
	if b.counters[name] == nil {
		b.counters[name] = make(map[string]float64)
	}
	b.counters[name][tagKey] += delta
}

// SetGauge implements metrics.Backend (generic).
//
// Any metric name is accepted and buffered.
// On Flush(), gauges are submitted as Datadog v2 GAUGE series.
//
// Notes:
//   - The latest value for a (metric, labels) pair wins within the flush window.
//   - Labels are converted to stable tags (sorted).
func (b *Backend) SetGauge(name string, value float64, labels metrics.Labels) {
	b.mu.Lock()
	defer b.mu.Unlock()

	tagKey := labelsKey(labels)
	if b.gauges[name] == nil {
		b.gauges[name] = make(map[string]float64)
	}
	b.gauges[name][tagKey] = value
}

// ObserveHistogram implements metrics.Backend.
//
// This remains intentionally opinionated: only known histogram names are buffered.
// On Flush(), we submit percentile gauges (p50/p90/p95/p99/max/samples) via v2.
//
// This keeps ETL and HTTP duration charts stable and low-overhead.
func (b *Backend) ObserveHistogram(name string, value float64, labels metrics.Labels) {
	if value < 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	switch name {
	case "etl_step_duration_seconds":
		step := labels["step"]
		status := labels["status"]
		k := stepStatusKey(step, status)
		b.durationSamples[k] = append(b.durationSamples[k], value)

	case "etl_http_request_duration_seconds":
		status := labels["status"]
		if status == "" {
			status = "unknown"
		}
		b.httpReqDur[status] = append(b.httpReqDur[status], value)

	case "etl_http_response_duration_seconds":
		status := labels["status"]
		if status == "" {
			status = "unknown"
		}
		b.httpRespDur[status] = append(b.httpRespDur[status], value)

	case "etl_http_download_bytes":
		status := labels["status"]
		if status == "" {
			status = "unknown"
		}
		b.httpDownloadB[status] = append(b.httpDownloadB[status], value)

	default:
		// Ignore unknown histograms by design.
	}
}

// ObserveDistribution records a native distribution sample (Datadog distribution points API).
func (b *Backend) ObserveDistribution(name string, value float64, labels metrics.Labels) {
	if value < 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	tagKey := labelsKey(labels)

	if b.distributions[name] == nil {
		b.distributions[name] = make(map[string][]float64)
	}

	b.distributions[name][tagKey] = append(b.distributions[name][tagKey], value)
}

// snapshot is the immutable set of buffered metric state used to build a flush payload.
type snapshot struct {
	counters map[string]map[string]float64
	gauges   map[string]map[string]float64

	durationSamples map[string][]float64

	httpReqDur    map[string][]float64
	httpRespDur   map[string][]float64
	httpDownloadB map[string][]float64

	distributions map[string]map[string][]float64
}

// snapshotAndReset grabs current buffered metrics and resets internal buffers.
func (b *Backend) snapshotAndReset() snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := snapshot{
		counters: b.counters,
		gauges:   b.gauges,

		durationSamples: b.durationSamples,

		httpReqDur:    b.httpReqDur,
		httpRespDur:   b.httpRespDur,
		httpDownloadB: b.httpDownloadB,

		distributions: b.distributions,
	}

	// Reset buffers for the next collection window.
	b.counters = make(map[string]map[string]float64)
	b.gauges = make(map[string]map[string]float64)

	b.durationSamples = make(map[string][]float64)

	b.httpReqDur = make(map[string][]float64)
	b.httpRespDur = make(map[string][]float64)
	b.httpDownloadB = make(map[string][]float64)

	b.distributions = make(map[string]map[string][]float64)

	return s
}

// isEmpty returns true if the snapshot contains no data to submit.
func (s snapshot) isEmpty() bool {
	return len(s.counters) == 0 &&
		len(s.gauges) == 0 &&
		len(s.durationSamples) == 0 &&
		len(s.httpReqDur) == 0 &&
		len(s.httpRespDur) == 0 &&
		len(s.httpDownloadB) == 0 &&
		len(s.distributions) == 0
}

// Flush submits buffered metrics to Datadog and resets local buffers.
//
// Behavior:
//   - v2 SubmitMetrics for count/gauge series (generic counters/gauges + percentile gauges)
//   - v1 SubmitDistributionPoints for distribution samples
func (b *Backend) Flush() error {
	snap := b.snapshotAndReset()
	if snap.isEmpty() {
		return nil
	}

	nowUnix := b.now().Unix()

	// --- v2 series (count/gauge + percentile gauges) ---
	v2Series := b.buildV2Series(snap, nowUnix)
	if len(v2Series) > 0 {
		payload := datadogV2.MetricPayload{Series: v2Series}
		_, resp, err := b.apiV2.SubmitMetrics(b.ctx, payload, *datadogV2.NewSubmitMetricsOptionalParameters())
		if resp != nil {
			fmt.Printf("datadog v2 submit status: %s\n", resp.Status)
		}
		if err != nil {
			return err
		}
	}

	// --- v1 distributions ---
	v1Payload := b.buildV1Distributions(snap, nowUnix)
	if len(v1Payload.Series) > 0 {
		_, resp, err := b.apiDist.SubmitDistributionPoints(b.ctx, v1Payload, *datadogV1.NewSubmitDistributionPointsOptionalParameters())
		if resp != nil {
			fmt.Printf("datadog v1 dist submit status: %s\n", resp.Status)
		}
		if err != nil {
			return err
		}
	}

	return nil
}

// buildV2Series constructs Datadog v2 series for COUNT/GAUGE metrics.
//
// This includes:
//   - generic counters (COUNT)
//   - generic gauges (GAUGE)
//   - percentile gauges derived from selected histograms (GAUGE)
func (b *Backend) buildV2Series(s snapshot, nowUnix int64) []datadogV2.MetricSeries {
	addCount := func(metric string, value float64, tags []string) datadogV2.MetricSeries {
		return datadogV2.MetricSeries{
			Metric: metric,
			Type:   datadogV2.METRICINTAKETYPE_COUNT.Ptr(),
			Points: []datadogV2.MetricPoint{
				{Timestamp: dd.PtrInt64(nowUnix), Value: dd.PtrFloat64(value)},
			},
			Tags: tags,
		}
	}

	addGauge := func(metric string, value float64, tags []string) datadogV2.MetricSeries {
		return datadogV2.MetricSeries{
			Metric: metric,
			Type:   datadogV2.METRICINTAKETYPE_GAUGE.Ptr(),
			Points: []datadogV2.MetricPoint{
				{Timestamp: dd.PtrInt64(nowUnix), Value: dd.PtrFloat64(value)},
			},
			Tags: tags,
		}
	}

	// Allocate with a reasonable lower bound.
	series := make([]datadogV2.MetricSeries, 0, 128)

	// --- Generic counters (COUNT) ---
	for metric, tagGroups := range s.counters {
		outMetric := mapMetricName(metric)
		for tagKey, v := range tagGroups {
			if v == 0 {
				continue
			}
			tags := withTags(b.baseTags, parseTagKey(tagKey)...)
			series = append(series, addCount(outMetric, v, tags))
		}
	}

	// --- Generic gauges (GAUGE) ---
	for metric, tagGroups := range s.gauges {
		outMetric := mapMetricName(metric)
		for tagKey, v := range tagGroups {
			tags := withTags(b.baseTags, parseTagKey(tagKey)...)
			series = append(series, addGauge(outMetric, v, tags))
		}
	}

	// --- Step duration percentiles (GAUGE) ---
	for k, samples := range s.durationSamples {
		addPercentiles(&series, b.baseTags, "etl.step.duration_seconds", "step_status", k, samples, nowUnix)
	}

	// --- HTTP percentiles (GAUGE) ---
	for status, samples := range s.httpReqDur {
		addPercentilesWithStatus(&series, b.baseTags, "etl.http.request_duration_seconds", status, samples, addGauge, nowUnix)
	}
	for status, samples := range s.httpRespDur {
		addPercentilesWithStatus(&series, b.baseTags, "etl.http.response_duration_seconds", status, samples, addGauge, nowUnix)
	}
	for status, samples := range s.httpDownloadB {
		addPercentilesWithStatus(&series, b.baseTags, "etl.http.download_bytes", status, samples, addGauge, nowUnix)
	}

	return series
}

// buildV1Distributions constructs a v1 DistributionPointsPayload from buffered samples.
//
// The v1 API expects:
//
//	points: [
//	  [
//	    timestamp,
//	    [v1, v2, v3]
//	  ]
//	]
//
// In this SDK version, DistributionPointItem is a oneOf wrapper type,
// meaning timestamp and values must be separate elements.
func (b *Backend) buildV1Distributions(s snapshot, nowUnix int64) datadogV1.DistributionPointsPayload {
	series := make([]datadogV1.DistributionPointsSeries, 0, len(s.distributions))

	for metric, tagGroups := range s.distributions {
		for tagKey, samples := range tagGroups {
			if len(samples) == 0 {
				continue
			}

			tags := withTags(b.baseTags, parseTagKey(tagKey)...)

			ts := float64(nowUnix)
			values := append([]float64(nil), samples...)

			series = append(series, datadogV1.DistributionPointsSeries{
				Metric: metric,
				Points: [][]datadogV1.DistributionPointItem{
					{
						{DistributionPointTimestamp: &ts},
						{DistributionPointData: &values},
					},
				},
				Tags: tags,
			})
		}
	}

	return datadogV1.DistributionPointsPayload{
		Series: series,
	}
}

// mapMetricName preserves backwards compatibility for established dashboards.
//
// Internal metric names (snake-ish) used by the core ETL package are mapped
// to dotted Datadog metric names. New metrics (like queue.tasks.created)
// are passed through unchanged.
func mapMetricName(name string) string {
	switch name {
	case "etl_step_total":
		return "etl.step.total"
	case "etl_records_total":
		return "etl.records.total"
	case "etl_batches_total":
		return "etl.batches.total"
	case "etl_http_requests_total":
		return "etl.http.requests.total"
	case "etl_http_errors_total":
		return "etl.http.errors.total"
	default:
		return name
	}
}

// addPercentiles appends a fixed set of percentile gauges for a sample set.
func addPercentiles(series *[]datadogV2.MetricSeries, baseTags []string, metricPrefix, keyKind, key string, samples []float64, nowUnix int64) {
	if len(samples) == 0 {
		return
	}
	cp := append([]float64(nil), samples...)
	sort.Float64s(cp)

	step, status := splitStepStatusKey(key)
	tags := withTags(baseTags, "step:"+step, "status:"+status)

	*series = append(*series, gaugeSeries(metricPrefix+".p50", percentileNearestRank(cp, 0.50), tags, nowUnix))
	*series = append(*series, gaugeSeries(metricPrefix+".p90", percentileNearestRank(cp, 0.90), tags, nowUnix))
	*series = append(*series, gaugeSeries(metricPrefix+".p95", percentileNearestRank(cp, 0.95), tags, nowUnix))
	*series = append(*series, gaugeSeries(metricPrefix+".p99", percentileNearestRank(cp, 0.99), tags, nowUnix))
	*series = append(*series, gaugeSeries(metricPrefix+".max", cp[len(cp)-1], tags, nowUnix))
	*series = append(*series, gaugeSeries(metricPrefix+".samples", float64(len(cp)), tags, nowUnix))

	_ = keyKind // reserved for future key schemas
}

func addPercentilesWithStatus(
	series *[]datadogV2.MetricSeries,
	baseTags []string,
	metricPrefix string,
	status string,
	samples []float64,
	addGauge func(metric string, value float64, tags []string) datadogV2.MetricSeries,
	nowUnix int64,
) {
	if len(samples) == 0 {
		return
	}
	cp := append([]float64(nil), samples...)
	sort.Float64s(cp)

	tags := withTags(baseTags, "status:"+status)
	*series = append(*series, addGauge(metricPrefix+".p50", percentileNearestRank(cp, 0.50), tags))
	*series = append(*series, addGauge(metricPrefix+".p90", percentileNearestRank(cp, 0.90), tags))
	*series = append(*series, addGauge(metricPrefix+".p95", percentileNearestRank(cp, 0.95), tags))
	*series = append(*series, addGauge(metricPrefix+".p99", percentileNearestRank(cp, 0.99), tags))
	*series = append(*series, addGauge(metricPrefix+".max", cp[len(cp)-1], tags))
	*series = append(*series, addGauge(metricPrefix+".samples", float64(len(cp)), tags))

	_ = nowUnix // reserved for future if addGauge is replaced by gaugeSeries
}

func gaugeSeries(metric string, value float64, tags []string, nowUnix int64) datadogV2.MetricSeries {
	return datadogV2.MetricSeries{
		Metric: metric,
		Type:   datadogV2.METRICINTAKETYPE_GAUGE.Ptr(),
		Points: []datadogV2.MetricPoint{
			{Timestamp: dd.PtrInt64(nowUnix), Value: dd.PtrFloat64(value)},
		},
		Tags: tags,
	}
}

func stepStatusKey(step, status string) string {
	return step + "\x00" + status
}

func splitStepStatusKey(k string) (step, status string) {
	parts := strings.SplitN(k, "\x00", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return k, "unknown"
}

func withTags(base []string, extras ...string) []string {
	out := make([]string, 0, len(base)+len(extras))
	out = append(out, base...)
	out = append(out, extras...)
	return out
}

func percentileNearestRank(s []float64, p float64) float64 {
	n := len(s)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return s[0]
	}
	if p >= 1 {
		return s[n-1]
	}
	idx := int(p*float64(n-1) + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return s[idx]
}

var _ metrics.Backend = (*Backend)(nil)

// ParseTagsCSV parses comma-separated tags like "env:prod,service:etl".
func ParseTagsCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func wrapInitErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("datadog metrics init: %w", err)
}

// labelsKey converts metrics.Labels into a stable, sorted tag key string.
func labelsKey(lbls metrics.Labels) string {
	if len(lbls) == 0 {
		return ""
	}

	keys := make([]string, 0, len(lbls))
	for k := range lbls {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+":"+lbls[k])
	}

	return strings.Join(parts, ",")
}

// parseTagKey reverses labelsKey back into a slice of tags.
func parseTagKey(k string) []string {
	if k == "" {
		return nil
	}
	return strings.Split(k, ",")
}
