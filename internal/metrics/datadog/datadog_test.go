package datadog

import (
	"context"
	"errors"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"etl/internal/metrics"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// fakeSubmitter captures payloads submitted by Backend.Flush().
type fakeSubmitter struct {
	mu       sync.Mutex
	payloads []datadogV2.MetricPayload
	err      error
}

func (f *fakeSubmitter) SubmitMetrics(ctx context.Context, body datadogV2.MetricPayload, params ...datadogV2.SubmitMetricsOptionalParameters) (datadogV2.IntakePayloadAccepted, *http.Response, error) {

	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = append(f.payloads, body)
	return datadogV2.IntakePayloadAccepted{}, nil, f.err
}

func (f *fakeSubmitter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.payloads)
}

func (f *fakeSubmitter) last() (datadogV2.MetricPayload, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.payloads) == 0 {
		return datadogV2.MetricPayload{}, false
	}
	return f.payloads[len(f.payloads)-1], true
}

// TestResolveEnvTag verifies environment-tag precedence and defaults.
//
// Edge cases:
//   - ENV wins over DD_ENV.
//   - Whitespace-only env vars are ignored.
//   - If neither is set, "env:unknown" is returned.
func TestResolveEnvTag(t *testing.T) {
	oldENV := os.Getenv("ENV")
	oldDDENV := os.Getenv("DD_ENV")
	t.Cleanup(func() {
		_ = os.Setenv("ENV", oldENV)
		_ = os.Setenv("DD_ENV", oldDDENV)
	})

	tests := []struct {
		name string
		env  string
		dd   string
		want string
	}{
		{name: "ENV_wins", env: "prod", dd: "stage", want: "env:prod"},
		{name: "DD_ENV_used_when_ENV_empty", env: "", dd: "stage", want: "env:stage"},
		{name: "whitespace_ignored", env: "   ", dd: "\n\t", want: "env:unknown"},
		{name: "default_unknown", env: "", dd: "", want: "env:unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Setenv("ENV", tc.env)
			_ = os.Setenv("DD_ENV", tc.dd)
			if got := resolveEnvTag(); got != tc.want {
				t.Fatalf("resolveEnvTag()=%q, want %q", got, tc.want)
			}
		})
	}
}

// TestWrapInitErr verifies error wrapping behavior.
//
// When to use:
//   - Protects stable error prefixing for init failures.
func TestWrapInitErr(t *testing.T) {
	if got := wrapInitErr(nil); got != nil {
		t.Fatalf("wrapInitErr(nil)=%v, want nil", got)
	}

	in := errors.New("boom")
	got := wrapInitErr(in)
	if got == nil {
		t.Fatalf("wrapInitErr(err)=nil, want non-nil")
	}
	if !strings.Contains(got.Error(), "datadog metrics init:") {
		t.Fatalf("wrapInitErr prefix missing: %v", got)
	}
	if !errors.Is(got, in) {
		t.Fatalf("wrapInitErr did not wrap original error: got=%v", got)
	}
}

// TestStepStatusKeyRoundTrip verifies key encoding/decoding.
func TestStepStatusKeyRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		step   string
		status string
	}{
		{name: "normal", step: "ddl", status: "ok"},
		{name: "empty_step", step: "", status: "ok"},
		{name: "empty_status", step: "pass2", status: ""},
		{name: "both_empty", step: "", status: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := stepStatusKey(tc.step, tc.status)
			step, status := splitStepStatusKey(k)
			if step != tc.step || status != tc.status {
				t.Fatalf("roundtrip got=(%q,%q), want=(%q,%q)", step, status, tc.step, tc.status)
			}
		})
	}

	t.Run("split_without_separator_defaults_unknown_status", func(t *testing.T) {
		step, status := splitStepStatusKey("no-sep")
		if step != "no-sep" || status != "unknown" {
			t.Fatalf("splitStepStatusKey()=(%q,%q), want=(%q,%q)", step, status, "no-sep", "unknown")
		}
	})
}

// TestWithTags verifies tag concatenation and immutability.
func TestWithTags(t *testing.T) {
	base := []string{"env:test", "job:etl"}
	extras := []string{"step:ddl", "status:ok"}
	got := withTags(base, extras...)
	want := []string{"env:test", "job:etl", "step:ddl", "status:ok"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withTags()=%v, want %v", got, want)
	}
	if !reflect.DeepEqual(base, []string{"env:test", "job:etl"}) {
		t.Fatalf("withTags mutated base: %v", base)
	}
	got[0] = "env:mutated"
	if base[0] == "env:mutated" {
		t.Fatalf("withTags output aliases base slice; base should not change when output is modified")
	}
}

// TestPercentileNearestRank verifies percentile behavior.
func TestPercentileNearestRank(t *testing.T) {
	tests := []struct {
		name string
		s    []float64
		p    float64
		want float64
	}{
		{name: "empty", s: nil, p: 0.50, want: 0},
		{name: "single", s: []float64{7}, p: 0.95, want: 7},
		{name: "p_le_0", s: []float64{1, 2, 3}, p: -1, want: 1},
		{name: "p_ge_1", s: []float64{1, 2, 3}, p: 2, want: 3},
		{name: "median", s: []float64{1, 2, 3, 4, 5}, p: 0.50, want: 3},
		{name: "p90_small_n", s: []float64{1, 2, 3, 4, 5}, p: 0.90, want: 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentileNearestRank(tc.s, tc.p); got != tc.want {
				t.Fatalf("percentileNearestRank(%v,%v)=%v, want %v", tc.s, tc.p, got, tc.want)
			}
		})
	}
}

// TestGaugeSeries verifies gaugeSeries timestamps and values.
func TestGaugeSeries(t *testing.T) {
	now := int64(1234567)
	s := gaugeSeries("etl.test.gauge", 3.14, []string{"env:test"}, now)

	if s.Metric != "etl.test.gauge" {
		t.Fatalf("Metric=%q, want %q", s.Metric, "etl.test.gauge")
	}
	if s.Type == nil || *s.Type != datadogV2.METRICINTAKETYPE_GAUGE {
		t.Fatalf("Type=%v, want GAUGE", s.Type)
	}
	if len(s.Points) != 1 {
		t.Fatalf("Points.len=%d, want 1", len(s.Points))
	}
	if s.Points[0].Timestamp == nil || *s.Points[0].Timestamp != now {
		t.Fatalf("Timestamp=%v, want %d", s.Points[0].Timestamp, now)
	}
	if s.Points[0].Value == nil || *s.Points[0].Value != 3.14 {
		t.Fatalf("Value=%v, want 3.14", s.Points[0].Value)
	}
}

// TestAddPercentiles verifies addPercentiles produces the expected series and does not mutate input.
//
// Coverage target:
//   - addPercentiles
func TestAddPercentiles(t *testing.T) {
	now := int64(999)
	base := []string{"env:test", "job:etl"}
	key := stepStatusKey("pass2", "ok")

	orig := []float64{5, 1, 3, 2, 4}
	in := append([]float64(nil), orig...) // preserve for mutation check

	var series []datadogV2.MetricSeries
	addPercentiles(&series, base, "etl.step.duration_seconds", "step_status", key, in, now)

	// Expect 6 gauges: p50,p90,p95,p99,max,samples
	if len(series) != 6 {
		t.Fatalf("series.len=%d, want 6", len(series))
	}

	// Verify input not mutated (addPercentiles sorts a copy).
	if !reflect.DeepEqual(in, orig) {
		t.Fatalf("samples mutated: got %v, want %v", in, orig)
	}

	// Verify sample count gauge exists.
	var foundSamples bool
	for _, s := range series {
		if s.Metric == "etl.step.duration_seconds.samples" {
			foundSamples = true
			if s.Points[0].Value == nil || *s.Points[0].Value != 5 {
				t.Fatalf("samples gauge value=%v, want 5", s.Points[0].Value)
			}
			break
		}
	}
	if !foundSamples {
		t.Fatalf("did not find samples gauge series")
	}
}

// TestAddPercentilesWithStatus verifies the status-tag percentile builder.
//
// Coverage target:
//   - addPercentilesWithStatus
func TestAddPercentilesWithStatus(t *testing.T) {
	now := int64(111) // not used currently, but reserved
	_ = now

	base := []string{"env:test", "job:etl"}
	status := "200"
	samples := []float64{10, 20, 30, 40, 50}

	// Use an addGauge closure that records calls; this is also how buildSeries wires it.
	var got []datadogV2.MetricSeries
	addGauge := func(metric string, value float64, tags []string) datadogV2.MetricSeries {
		// Use a fixed timestamp to keep deterministic; the specific timestamp isn't
		// what we're testing here.
		return gaugeSeries(metric, value, tags, 1)
	}

	addPercentilesWithStatus(&got, base, "etl.http.request_duration_seconds", status, samples, addGauge, 1)

	if len(got) != 6 {
		t.Fatalf("series.len=%d, want 6", len(got))
	}
	// Ensure tags include status
	for _, s := range got {
		if !contains(s.Tags, "status:200") {
			t.Fatalf("series %q missing status tag; tags=%v", s.Metric, s.Tags)
		}
	}
}

// TestNewBackend_Defaults verifies defaults and initialization behavior without real HTTP.
//
// Coverage target:
//   - NewBackend
func TestNewBackend_Defaults(t *testing.T) {
	fs := &fakeSubmitter{}
	opts := Options{
		JobName:    "", // should default
		FlushEvery: 0,  // should default
		Tags:       []string{"service:etl"},
		submitter:  fs,
		now:        func() time.Time { return time.Unix(123, 0) },
		newTicker:  func(d time.Duration) *time.Ticker { return time.NewTicker(24 * time.Hour) }, // effectively disables loop in this test
	}

	b, err := NewBackend(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewBackend() err=%v, want nil", err)
	}
	defer func() { _ = b.Close() }()

	// baseTags should include env tag + job tag + provided tags.
	// env tag depends on env vars; we just require "job:etl" exists and "service:etl" exists.
	if !contains(b.baseTags, "job:etl") {
		t.Fatalf("baseTags missing job:etl: %v", b.baseTags)
	}
	if !contains(b.baseTags, "service:etl") {
		t.Fatalf("baseTags missing service:etl: %v", b.baseTags)
	}
	if b.flushEvery != 60*time.Second {
		t.Fatalf("flushEvery=%s, want 60s", b.flushEvery)
	}
}

// TestFlush_SubmitsAndResets verifies Flush submits buffered metrics and resets buffers.
//
// Coverage target:
//   - Flush
func TestFlush_SubmitsAndResets(t *testing.T) {
	fs := &fakeSubmitter{}
	opts := Options{
		JobName:    "job1",
		FlushEvery: 24 * time.Hour, // minimize loop behavior
		submitter:  fs,
		now:        func() time.Time { return time.Unix(1000, 0) },
		newTicker:  func(d time.Duration) *time.Ticker { return time.NewTicker(24 * time.Hour) },
	}

	b, err := NewBackend(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewBackend() err=%v", err)
	}
	defer func() { _ = b.Close() }()

	b.IncCounter("etl_step_total", 2, metrics.Labels{"step": "ddl", "status": "ok"})
	b.IncCounter("etl_records_total", 3, metrics.Labels{"kind": "inserted"})
	b.IncCounter("etl_batches_total", 1, nil)
	b.ObserveHistogram("etl_step_duration_seconds", 0.5, metrics.Labels{"step": "ddl", "status": "ok"})
	b.IncCounter("etl_http_requests_total", 7, metrics.Labels{"status": "200"})
	b.ObserveHistogram("etl_http_request_duration_seconds", 0.1, metrics.Labels{"status": "200"})

	if err := b.Flush(); err != nil {
		t.Fatalf("Flush() err=%v, want nil", err)
	}
	if fs.count() != 1 {
		t.Fatalf("submit calls=%d, want 1", fs.count())
	}

	// Buffers should be reset after flush.
	if len(b.durationSamples) != 0 {
		t.Fatalf("buffers not reset after Flush")
	}

	// Validate payload contains expected metrics.
	payload, ok := fs.last()
	if !ok {
		t.Fatalf("missing payload")
	}

	var metricNames []string
	for _, s := range payload.Series {
		metricNames = append(metricNames, s.Metric)
	}
	sort.Strings(metricNames)

	// We expect:
	// - etl.step.total
	// - etl.records.total
	// - etl.batches.total
	// - step duration percentile gauges (p50,p90,p95,p99,max,samples)
	// - http request count
	// - http request duration percentile gauges (p50,p90,p95,p99,max,samples)
	//
	// This test only asserts presence of key series names that represent the contract.
	wantContains := []string{
		"etl.batches.total",
		"etl.records.total",
		"etl.step.total",
		"etl.step.duration_seconds.p50",
		"etl.step.duration_seconds.samples",
		"etl.http.requests.total",
		"etl.http.request_duration_seconds.p50",
		"etl.http.request_duration_seconds.samples",
	}
	for _, w := range wantContains {
		if !contains(metricNames, w) {
			t.Fatalf("payload missing metric %q; got=%v", w, metricNames)
		}
	}
}

// TestFlush_NoDataDoesNotSubmit verifies Flush returns nil and does not submit when empty.
//
// Coverage target:
//   - Flush empty-path
func TestFlush_NoDataDoesNotSubmit(t *testing.T) {
	fs := &fakeSubmitter{}
	opts := Options{
		JobName:    "job1",
		FlushEvery: 24 * time.Hour,
		submitter:  fs,
		now:        func() time.Time { return time.Unix(1000, 0) },
		newTicker:  func(d time.Duration) *time.Ticker { return time.NewTicker(24 * time.Hour) },
	}

	b, err := NewBackend(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewBackend() err=%v", err)
	}
	defer func() { _ = b.Close() }()

	if err := b.Flush(); err != nil {
		t.Fatalf("Flush() err=%v, want nil", err)
	}
	if fs.count() != 0 {
		t.Fatalf("unexpected submission count=%d, want 0", fs.count())
	}
}

// TestLoopAndClose verifies the background loop flushes periodically and Close performs a final flush.
//
// Coverage target:
//   - loop
//   - Close
func TestLoopAndClose(t *testing.T) {
	fs := &fakeSubmitter{}

	// Use a fast ticker to trigger at least one background flush.
	opts := Options{
		JobName:    "job1",
		FlushEvery: 5 * time.Millisecond,
		submitter:  fs,
		now:        func() time.Time { return time.Unix(2000, 0) },
		// Use real ticker for this test (default), so loop is exercised.
	}

	b, err := NewBackend(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewBackend() err=%v", err)
	}

	// Put some data in the buffers; loop should flush it.
	b.IncCounter("etl_batches_total", 1, nil)

	// Wait briefly for at least one tick.
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if fs.count() >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if fs.count() < 1 {
		_ = b.Close()
		t.Fatalf("expected at least one background Flush submission; got %d", fs.count())
	}

	// Add more data; Close should perform a final flush.
	b.IncCounter("etl_batches_total", 1, nil)
	if err := b.Close(); err != nil {
		t.Fatalf("Close() err=%v, want nil", err)
	}

	// Close performs a final flush, so we expect at least 2 submissions total:
	// one from the periodic loop, one from Close()'s final Flush().
	if fs.count() < 2 {
		t.Fatalf("expected at least 2 submissions after Close; got %d", fs.count())
	}
}

// TestBackend_ConcurrentAccess verifies thread-safety of buffering.
// This also covers IncCounter/ObserveHistogram under race-like conditions.
func TestBackend_ConcurrentAccess(t *testing.T) {
	fs := &fakeSubmitter{}
	opts := Options{
		JobName:    "job1",
		FlushEvery: 24 * time.Hour,
		submitter:  fs,
		now:        func() time.Time { return time.Unix(3000, 0) },
		newTicker:  func(d time.Duration) *time.Ticker { return time.NewTicker(24 * time.Hour) },
	}
	b, err := NewBackend(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewBackend() err=%v", err)
	}
	defer func() { _ = b.Close() }()

	workers := runtime.GOMAXPROCS(0) * 4
	iters := 2000

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				b.IncCounter("etl_batches_total", 1, nil)
				b.IncCounter("etl_step_total", 1, metrics.Labels{"step": "pass2", "status": "ok"})
				b.IncCounter("etl_records_total", 1, metrics.Labels{"kind": "inserted"})
				b.ObserveHistogram("etl_step_duration_seconds", 0.01, metrics.Labels{"step": "pass2", "status": "ok"})
				b.ObserveHistogram("etl_http_request_duration_seconds", 0.02, metrics.Labels{"status": "200"})
			}
		}()
	}
	wg.Wait()

	// Force a flush and validate no panic and one submission.
	if err := b.Flush(); err != nil {
		t.Fatalf("Flush() err=%v, want nil", err)
	}
	if fs.count() != 1 {
		t.Fatalf("submit calls=%d, want 1", fs.count())
	}
}

// TestIncCounterAndObserveHistogram_EdgeCases verifies ignored paths and defaults.
func TestIncCounterAndObserveHistogram_EdgeCases(t *testing.T) {
	fs := &fakeSubmitter{}
	opts := Options{
		JobName:    "job1",
		FlushEvery: 24 * time.Hour,
		submitter:  fs,
		now:        func() time.Time { return time.Unix(4000, 0) },
		newTicker:  func(d time.Duration) *time.Ticker { return time.NewTicker(24 * time.Hour) },
	}
	b, err := NewBackend(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewBackend() err=%v", err)
	}
	defer func() { _ = b.Close() }()

	// Non-positive counter should be ignored.
	b.IncCounter("etl_batches_total", 0, nil)
	// Missing kind should be ignored.
	b.IncCounter("etl_records_total", 1, metrics.Labels{})
	// Unknown metric should be ignored.
	b.IncCounter("unknown_total", 1, metrics.Labels{"x": "y"})
	// Negative histogram should be ignored.
	b.ObserveHistogram("etl_step_duration_seconds", -1, metrics.Labels{"step": "ddl", "status": "ok"})
	// Missing status should default "unknown".
	b.IncCounter("etl_http_requests_total", 1, metrics.Labels{})
	b.ObserveHistogram("etl_http_request_duration_seconds", 0.1, metrics.Labels{})

	if err := b.Flush(); err != nil {
		t.Fatalf("Flush() err=%v, want nil", err)
	}

	payload, ok := fs.last()
	if !ok {
		t.Fatalf("missing payload")
	}

	// Should include http request count and duration percentiles for status:unknown.
	var sawHTTPCount bool
	var sawP50 bool
	for _, s := range payload.Series {
		if s.Metric == "etl.http.requests.total" && contains(s.Tags, "status:unknown") {
			sawHTTPCount = true
		}
		if s.Metric == "etl.http.request_duration_seconds.p50" && contains(s.Tags, "status:unknown") {
			sawP50 = true
		}
	}
	if !sawHTTPCount {
		t.Fatalf("expected etl.http.requests.total for status:unknown")
	}
	if !sawP50 {
		t.Fatalf("expected etl.http.request_duration_seconds.p50 for status:unknown")
	}
}

func contains[T comparable](xs []T, v T) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func TestParseTagsCSV(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "empty_returns_nil",
			in:   "",
			want: nil,
		},
		{
			name: "trims_and_skips_empty_segments",
			in:   " env:prod , ,service:etl,  ,team:data ",
			want: []string{"env:prod", "service:etl", "team:data"},
		},
		{
			name: "single_tag",
			in:   "service:etl",
			want: []string{"service:etl"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ParseTagsCSV(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseTagsCSV(%q)=%v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
