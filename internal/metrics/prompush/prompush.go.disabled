// Package prompush implements a Prometheus Pushgateway backend for the
// metrics package.
//
// This package adapts the generic metrics.Backend interface to Prometheus by:
//
//   - Using client_golang CounterVec and HistogramVec collectors.
//   - Mapping the common ETL labels (job, step, status) onto Prometheus labels.
//   - Pushing collected metrics to a Prometheus Pushgateway instance instead of
//     exposing an HTTP scrape endpoint.
//
// The package intentionally contains all Prometheus-specific dependencies so
// that the rest of the project remains decoupled from Prometheus and can swap
// to alternative backends (e.g. Datadog, StatsD) without changes to the core
// ETL pipeline.
package prompush

import (
	"fmt"

	"etl/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

// Backend is a Prometheus Pushgateway metrics backend.
type Backend struct {
	gatewayURL string // e.g. http://pushgateway:9091
	jobName    string // Pushgateway "job" group
	reg        *prometheus.Registry

	// Step-level metrics
	stepCounter *prometheus.CounterVec // "etl_step_total"
	//stepHist    *prometheus.HistogramVec // "etl_step_duration_seconds"
	stepDuration *prometheus.SummaryVec // etl_step_duration_seconds (summary)

	// Record-level metrics (summary-like)
	recordCounter *prometheus.CounterVec // "etl_records_total"
	batchCounter  prometheus.Counter     // "etl_batches_total"
}

// NewBackend constructs a Prometheus Pushgateway backend.
// jobName: the Pushgateway "job" name (often same as pipeline job).
// gatewayURL: base URL of the Pushgateway server.
func NewBackend(jobName, gatewayURL string) (*Backend, error) {
	if gatewayURL == "" {
		return nil, fmt.Errorf("prompush: gateway URL is required")
	}
	if jobName == "" {
		jobName = "etl"
	}

	reg := prometheus.NewRegistry()

	// We use job, step, status as dynamic labels;
	// job is also used as the Pushgateway "job" grouping key.
	stepCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "etl_step_total",
			Help: "Total number of ETL step executions, partitioned by job, step, and status.",
		},
		[]string{"step", "status"},
	)
	stepDuration := prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "etl_step_duration_seconds",
			Help:       "Duration of ETL steps in seconds, partitioned by step and status.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		[]string{"step", "status"}, // still 2 labels
	)

	// RECORD metrics: kind (processed, parse_errors, inserted, ...).
	recordCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "etl_records_total",
			Help: "Record-level counts per kind (processed, parse_errors, inserted, etc.).",
		},
		[]string{"kind"},
	)

	// BATCH metrics: simple counter per job (job is grouping label via Pushgateway).
	batchCounter := prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "etl_batches_total",
			Help: "Total number of COPY batches flushed for this ETL job.",
		},
	)

	if err := reg.Register(stepCounter); err != nil {
		return nil, fmt.Errorf("prompush: register step counter: %w", err)
	}
	if err := reg.Register(stepDuration); err != nil {
		return nil, fmt.Errorf("prompush: register step summary: %w", err)
	}
	if err := reg.Register(recordCounter); err != nil {
		return nil, fmt.Errorf("prompush: register record counter: %w", err)
	}
	if err := reg.Register(batchCounter); err != nil {
		return nil, fmt.Errorf("prompush: register batch counter: %w", err)
	}

	return &Backend{
		gatewayURL:    gatewayURL,
		jobName:       jobName,
		reg:           reg,
		stepCounter:   stepCounter,
		stepDuration:  stepDuration,
		recordCounter: recordCounter,
		batchCounter:  batchCounter,
	}, nil
}

func (b *Backend) IncCounter(name string, delta float64, labels metrics.Labels) {
	switch name {
	case "etl_step_total":
		if b.stepCounter == nil {
			return
		}
		step := labels["step"]
		status := labels["status"]
		b.stepCounter.WithLabelValues(step, status).Add(delta)

	case "etl_records_total":
		if b.recordCounter == nil {
			return
		}
		kind := labels["kind"]
		b.recordCounter.WithLabelValues(kind).Add(delta)

	case "etl_batches_total":
		if b.batchCounter == nil {
			return
		}
		b.batchCounter.Add(delta)

	default:
		// unknown metric name: ignore
	}
}

func (b *Backend) ObserveHistogram(name string, value float64, labels metrics.Labels) {
	if name != "etl_step_duration_seconds" || b.stepDuration == nil {
		return
	}
	step := labels["step"]
	status := labels["status"]
	b.stepDuration.WithLabelValues(step, status).Observe(value)
}

// Flush pushes the current registry to the Pushgateway.
func (b *Backend) Flush() error {
	return push.New(b.gatewayURL, b.jobName).
		Gatherer(b.reg).
		Push()
}
