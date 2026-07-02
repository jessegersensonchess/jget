// Package prompush_test contains unit tests and benchmarks for the prompush package.
package prompush

import (
	"etl/internal/metrics"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// readCounterValue reads the current value of a Counter for assertions in tests.
func readCounterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()

	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("Counter.Write() error = %v", err)
	}
	if m.GetCounter() == nil {
		t.Fatalf("metric did not contain Counter value")
	}
	return m.GetCounter().GetValue()
}

// readSummaryCountSum reads sample count and sum from a SummaryVec for assertions in tests.
func readSummaryCountSum(t *testing.T, v *prometheus.SummaryVec, labels ...string) (uint64, float64) {
	t.Helper()

	m := &dto.Metric{}
	metric, ok := v.WithLabelValues(labels...).(prometheus.Metric)
	if !ok {
		t.Fatalf("SummaryVec.WithLabelValues(...) does not implement prometheus.Metric")
	}
	if err := metric.Write(m); err != nil {
		t.Fatalf("Summary.Write() error = %v", err)
	}
	if m.GetSummary() == nil {
		t.Fatalf("metric did not contain Summary value")
	}
	sum := m.GetSummary()
	return sum.GetSampleCount(), sum.GetSampleSum()
}

// TestNewBackend constructs backends with different inputs and validates
// field initialization, defaults, and basic metric usability.
func TestNewBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		jobName     string
		gatewayURL  string
		wantErr     bool
		wantJobName string
	}{
		{
			name:       "missing gateway URL returns error",
			jobName:    "etl-job",
			gatewayURL: "",
			wantErr:    true,
		},
		{
			name:        "empty job name uses default",
			jobName:     "",
			gatewayURL:  "http://pushgateway:9091",
			wantErr:     false,
			wantJobName: "etl",
		},
		{
			name:        "explicit job name is preserved",
			jobName:     "my-custom-job",
			gatewayURL:  "http://pushgateway:9091",
			wantErr:     false,
			wantJobName: "my-custom-job",
		},
	}

	for _, tt := range tests {
		tt := tt // capture
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b, err := NewBackend(tt.jobName, tt.gatewayURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewBackend(%q, %q) error = nil, want non-nil", tt.jobName, tt.gatewayURL)
				}
				if b != nil {
					t.Fatalf("NewBackend(%q, %q) backend = %v, want nil", tt.jobName, tt.gatewayURL, b)
				}
				return
			}

			if err != nil {
				t.Fatalf("NewBackend(%q, %q) error = %v, want nil", tt.jobName, tt.gatewayURL, err)
			}
			if b == nil {
				t.Fatalf("NewBackend(%q, %q) backend = nil, want non-nil", tt.jobName, tt.gatewayURL)
			}

			if b.jobName != tt.wantJobName {
				t.Fatalf("backend.jobName = %q, want %q", b.jobName, tt.wantJobName)
			}
			if b.gatewayURL != tt.gatewayURL {
				t.Fatalf("backend.gatewayURL = %q, want %q", b.gatewayURL, tt.gatewayURL)
			}

			// Basic sanity: metrics should be non-nil and accept the expected labels.
			if b.stepCounter == nil {
				t.Fatalf("stepCounter is nil")
			}
			if b.stepDuration == nil {
				t.Fatalf("stepDuration is nil")
			}
			if b.recordCounter == nil {
				t.Fatalf("recordCounter is nil")
			}
			if b.batchCounter == nil {
				t.Fatalf("batchCounter is nil")
			}

			// Metric label cardinality: these calls should not panic.
			b.stepCounter.WithLabelValues("load", "ok").Add(1)
			b.stepDuration.WithLabelValues("transform", "error").Observe(0.5)
			b.recordCounter.WithLabelValues("processed").Add(1)
			b.batchCounter.Add(1)
		})
	}
}

// TestIncCounter verifies that IncCounter routes updates to the correct
// Prometheus collectors and ignores unknown metric names.
func TestIncCounter(t *testing.T) {
	t.Parallel()

	type args struct {
		name   string
		delta  float64
		labels metrics.Labels
	}
	tests := []struct {
		name         string
		args         []args
		wantCounters func(t *testing.T, b *Backend)
	}{
		{
			name: "increments step counter with labels",
			args: []args{
				{
					name:  "etl_step_total",
					delta: 3,
					labels: metrics.Labels{
						"step":   "extract",
						"status": "ok",
					},
				},
			},
			wantCounters: func(t *testing.T, b *Backend) {
				got := readCounterValue(t, b.stepCounter.WithLabelValues("extract", "ok"))
				if got != 3 {
					t.Fatalf("stepCounter value = %v, want 3", got)
				}
			},
		},
		{
			name: "increments record counter with kind label",
			args: []args{
				{
					name:  "etl_records_total",
					delta: 5,
					labels: metrics.Labels{
						"kind": "processed",
					},
				},
			},
			wantCounters: func(t *testing.T, b *Backend) {
				got := readCounterValue(t, b.recordCounter.WithLabelValues("processed"))
				if got != 5 {
					t.Fatalf("recordCounter value = %v, want 5", got)
				}
			},
		},
		{
			name: "increments batch counter without labels",
			args: []args{
				{
					name:   "etl_batches_total",
					delta:  2,
					labels: metrics.Labels{},
				},
				{
					name:   "etl_batches_total",
					delta:  0.5,
					labels: metrics.Labels{},
				},
			},
			wantCounters: func(t *testing.T, b *Backend) {
				got := readCounterValue(t, b.batchCounter)
				if got != 2.5 {
					t.Fatalf("batchCounter value = %v, want 2.5", got)
				}
			},
		},
		{
			name: "unknown metric name is ignored",
			args: []args{
				{
					name:   "unknown_metric",
					delta:  10,
					labels: metrics.Labels{"foo": "bar"},
				},
			},
			wantCounters: func(t *testing.T, b *Backend) {
				if got := readCounterValue(t, b.batchCounter); got != 0 {
					t.Fatalf("batchCounter value = %v, want 0 (unchanged)", got)
				}
				// Also sanity-check a label combination that we never incremented.
				if got := readCounterValue(t, b.stepCounter.WithLabelValues("x", "y")); got != 0 {
					t.Fatalf("stepCounter value = %v, want 0 (unchanged)", got)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt // capture
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b, err := NewBackend("etl", "http://example.com")
			if err != nil {
				t.Fatalf("NewBackend() error = %v", err)
			}

			for _, a := range tt.args {
				b.IncCounter(a.name, a.delta, a.labels)
			}

			if tt.wantCounters != nil {
				tt.wantCounters(t, b)
			}
		})
	}
}

// TestIncCounterNilMetrics ensures that IncCounter is defensive when
// underlying metric collectors are missing, and does not panic.
func TestIncCounterNilMetrics(t *testing.T) {
	t.Parallel()

	b := &Backend{} // zero-value backend with nil collectors

	// These calls should all be safe no-ops.
	b.IncCounter("etl_step_total", 1, metrics.Labels{"step": "s", "status": "ok"})
	b.IncCounter("etl_records_total", 1, metrics.Labels{"kind": "processed"})
	b.IncCounter("etl_batches_total", 1, metrics.Labels{})
	b.IncCounter("unknown", 1, metrics.Labels{})
}

// TestObserveHistogram verifies that ObserveHistogram records observations
// on the summary-based step duration metric for valid inputs and ignores others.
func TestObserveHistogram(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		metricName  string
		value       float64
		labels      metrics.Labels
		nilDuration bool
		wantCount   uint64
		wantSum     float64
	}{
		{
			name:       "records duration for valid metric and labels",
			metricName: "etl_step_duration_seconds",
			value:      1.5,
			labels: metrics.Labels{
				"step":   "load",
				"status": "ok",
			},
			wantCount: 1,
			wantSum:   1.5,
		},
		{
			name:       "ignores unknown metric name",
			metricName: "other_metric",
			value:      2.0,
			labels: metrics.Labels{
				"step":   "load",
				"status": "ok",
			},
			wantCount: 0,
			wantSum:   0,
		},
		{
			name:        "skips observation when summary is nil",
			metricName:  "etl_step_duration_seconds",
			value:       3.0,
			labels:      metrics.Labels{"step": "load", "status": "ok"},
			nilDuration: true,
			wantCount:   0,
			wantSum:     0,
		},
	}

	for _, tt := range tests {
		tt := tt // capture
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b, err := NewBackend("etl", "http://example.com")
			if err != nil {
				t.Fatalf("NewBackend() error = %v", err)
			}
			if tt.nilDuration {
				b.stepDuration = nil
			}

			b.ObserveHistogram(tt.metricName, tt.value, tt.labels)

			if b.stepDuration == nil {
				// When summary is nil, there is nothing to read; expect zeroes.
				if tt.wantCount != 0 || tt.wantSum != 0 {
					t.Fatalf("expected no summary data but wantCount=%d wantSum=%v", tt.wantCount, tt.wantSum)
				}
				return
			}

			gotCount, gotSum := readSummaryCountSum(t, b.stepDuration, tt.labels["step"], tt.labels["status"])
			if gotCount != tt.wantCount {
				t.Fatalf("summary sample count = %d, want %d", gotCount, tt.wantCount)
			}
			if gotSum != tt.wantSum {
				t.Fatalf("summary sample sum = %v, want %v", gotSum, tt.wantSum)
			}
		})
	}
}

// TestFlush verifies that Flush pushes the registry to the configured
// Pushgateway URL by sending an HTTP request to the gateway.
func TestFlush(t *testing.T) {
	t.Parallel()

	type pushRequestInfo struct {
		method  string
		path    string
		bodyLen int
	}

	reqCh := make(chan pushRequestInfo, 1)

	// Fake Pushgateway server that records the incoming request.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)

		reqCh <- pushRequestInfo{
			method:  r.Method,
			path:    r.URL.Path,
			bodyLen: len(body),
		}
		// Pushgateway typically returns 202 Accepted.
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	b, err := NewBackend("etl-job", server.URL)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}

	// Add some data so the push body is non-empty.
	b.IncCounter("etl_step_total", 1, metrics.Labels{"step": "extract", "status": "ok"})

	if err := b.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	var got pushRequestInfo
	select {
	case got = <-reqCh:
		// OK
	default:
		t.Fatalf("Flush() did not result in any HTTP request to the Pushgateway")
	}

	if got.method == "" {
		t.Fatalf("Push request method is empty")
	}
	if got.path == "" {
		t.Fatalf("Push request path is empty")
	}
	if got.bodyLen == 0 {
		t.Fatalf("Push request body length = 0, want > 0")
	}
}

// BenchmarkNewBackend measures the overhead of constructing and
// registering a new Backend (including a new Registry and collectors).
func BenchmarkNewBackend(b *testing.B) {
	for i := 0; i < b.N; i++ {
		backend, err := NewBackend("etl", "http://example.com")
		if err != nil {
			b.Fatalf("NewBackend() error = %v", err)
		}
		if backend.reg == nil {
			b.Fatalf("backend.reg is nil")
		}
	}
}

// BenchmarkIncCounterStep measures the cost of incrementing the step counter
// through the Backend IncCounter abstraction.
func BenchmarkIncCounterStep(b *testing.B) {
	backend, err := NewBackend("etl", "http://example.com")
	if err != nil {
		b.Fatalf("NewBackend() error = %v", err)
	}

	labels := metrics.Labels{"step": "extract", "status": "ok"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		backend.IncCounter("etl_step_total", 1, labels)
	}
}

// BenchmarkIncCounterRecord measures the cost of incrementing the record counter
// through the Backend IncCounter abstraction.
func BenchmarkIncCounterRecord(b *testing.B) {
	backend, err := NewBackend("etl", "http://example.com")
	if err != nil {
		b.Fatalf("NewBackend() error = %v", err)
	}

	labels := metrics.Labels{"kind": "processed"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		backend.IncCounter("etl_records_total", 1, labels)
	}
}

// BenchmarkObserveHistogram measures the cost of recording a step duration
// observation via ObserveHistogram.
func BenchmarkObserveHistogram(b *testing.B) {
	backend, err := NewBackend("etl", "http://example.com")
	if err != nil {
		b.Fatalf("NewBackend() error = %v", err)
	}

	labels := metrics.Labels{"step": "load", "status": "ok"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		backend.ObserveHistogram("etl_step_duration_seconds", 0.123, labels)
	}
}
