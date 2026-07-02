// Package metrics provides a small, backend-agnostic abstraction for recording
// operational metrics from the ETL pipeline.
//
// The package is intentionally minimal and opinionated:
//
//   - It exposes a narrow interface (Backend) focused on counters and timing
//     data (histograms).
//   - It provides a global, pluggable backend that defaults to a no-op
//     implementation, so metrics are always safe to call even when no real
//     backend is configured.
//   - It is designed to mirror the storage abstraction pattern used elsewhere
//     in the project (e.g. storage.Repository), allowing the rest of the codebase
//     to depend only on this interface while keeping concrete metric systems
//     isolated in subpackages.
//
// The primary use case is instrumentation of the ETL pipeline stages
// (reader, transformer, validator, loader, etc.) without coupling the core
// application logic to a specific metrics system such as Prometheus or Datadog.
package metrics

import (
	"strconv"
	"time"
)

// Labels are string key/value pairs attached to a metric.
type Labels map[string]string

// Backend is the minimal interface for metrics backends.
// It is intentionally generic so we can plug in Prometheus, Datadog, etc.
type Backend interface {
	// IncCounter increments a counter by delta.
	IncCounter(name string, delta float64, labels Labels)
	// ObserveHistogram records a value in a latency/duration style metric.
	ObserveHistogram(name string, value float64, labels Labels)
	// Flush pushes or flushes metrics, if the backend needs it (e.g. Pushgateway).
	Flush() error

	// SetGauge sets a gauge to an absolute value.
	SetGauge(name string, value float64, labels Labels)
}

// nopBackend is used by default so metrics are optional.
type nopBackend struct{}

func (nopBackend) IncCounter(name string, delta float64, labels Labels)       {}
func (nopBackend) ObserveHistogram(name string, value float64, labels Labels) {}
func (nopBackend) SetGauge(name string, value float64, labels Labels)         {}
func (nopBackend) Flush() error                                               { return nil }

// Counter increments a counter by delta (generic).
func Counter(name string, delta float64, labels Labels) {
	backend.IncCounter(name, delta, labels)
}

var backend Backend = nopBackend{}

// SetBackend installs a concrete backend. Passing nil keeps the existing backend.
func SetBackend(b Backend) {
	if b == nil {
		return
	}
	backend = b
}

// Flush delegates to the current backend.
func Flush() error {
	return backend.Flush()
}

// RecordStep is a convenience for the common pattern:
// measure latency + success/failure per ETL step.
func RecordStep(job, step string, err error, d time.Duration) {
	status := "success"
	if err != nil {
		status = "failure"
	}

	lbls := Labels{
		"job":    job,
		"step":   step,
		"status": status,
	}

	backend.IncCounter("etl_step_total", 1, lbls)
	backend.ObserveHistogram("etl_step_duration_seconds", d.Seconds(), lbls)
}

// Gauge sets a gauge metric to an absolute value.
//
// This is a thin wrapper over backend.SetGauge and is safe to call
// even when no real backend is configured.
func Gauge(name string, value float64, labels Labels) {
	backend.SetGauge(name, value, labels)
}

// RecordRow increments a record-level counter for the given job and kind.
//
// Typical kinds mirror the ETL summary fields, e.g.:
//   - "processed"
//   - "parse_errors"
//   - "validate_dropped"
//   - "transform_rejected"
//   - "transform_dropped"
//   - "inserted"
func RecordRow(job, kind string, delta int64) {
	if delta <= 0 {
		return
	}
	backend.IncCounter("etl_records_total", float64(delta), Labels{
		"job":  job,
		"kind": kind,
	})
}

// RecordBatches increments a batch-level counter for the given job.
func RecordBatches(job string, delta int64) {
	if delta <= 0 {
		return
	}
	backend.IncCounter("etl_batches_total", float64(delta), Labels{
		"job": job,
	})
}

// RecordHTTP records request-level telemetry for HTTP-driven ingestion.
//
// When to use:
//   - Any crawler, downloader, or HTTP-based extractor where you want:
//   - request/response latency distributions
//   - bytes distributions
//   - counters by HTTP status
//
// Labels:
//   - job: the logical pipeline/job name
//   - status: HTTP status code as a string ("200", "429", "0" for network errors)
//
// Edge cases:
//   - status <= 0 should be used for network/transport errors.
//   - Negative durations/bytes are ignored.
//
// Errors:
//   - err is used only to record a separate error counter.
//     Callers should still log the actual error for debugging.
func RecordHTTP(job string, status int, err error, requestDur, responseDur time.Duration, bytes int64) {
	lbls := Labels{
		"job":    job,
		"status": strconv.Itoa(status),
	}

	backend.IncCounter("etl_http_requests_total", 1, lbls)

	if err != nil {
		backend.IncCounter("etl_http_errors_total", 1, lbls)
	}

	if requestDur >= 0 {
		backend.ObserveHistogram("etl_http_request_duration_seconds", requestDur.Seconds(), lbls)
	}
	if responseDur >= 0 {
		backend.ObserveHistogram("etl_http_response_duration_seconds", responseDur.Seconds(), lbls)
	}
	if bytes >= 0 {
		backend.ObserveHistogram("etl_http_download_bytes", float64(bytes), lbls)
	}
}

// ObserveDistribution records a distribution-style metric sample.
//
// When to use:
//   - High-cardinality sample metrics such as durations, sizes, or processing times.
//   - Intended for backends that support native distribution metrics (e.g. Datadog).
//
// Labels:
//   - Free-form key/value pairs.
//   - Must remain low-cardinality in production use.
//
// Behavior:
//   - Delegates to backend.ObserveHistogram.
//   - Backends decide whether to treat it as histogram, gauge percentiles,
//     or native distribution.
//
// Safe to call even when no backend is configured.
// ObserveDistribution records a native distribution metric.
//
// This is intended for backends that support native distribution types
// (e.g. Datadog). Backends that do not support distributions may ignore it.
//
// Use this for high-volume duration metrics where backend-side
// percentile aggregation is preferred.
func ObserveDistribution(name string, value float64, labels Labels) {
	if value < 0 {
		return
	}
	if d, ok := backend.(interface {
		ObserveDistribution(string, float64, Labels)
	}); ok {
		d.ObserveDistribution(name, value, labels)
	}
}
