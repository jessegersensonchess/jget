package metrics

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeBackend is a simple in-memory Backend implementation for tests.
type fakeBackend struct {
	mu sync.Mutex

	callsCounters   []counterCall
	callsHistograms []histCall
	flushCount      int
}

type counterCall struct {
	name   string
	delta  float64
	labels Labels
}

type histCall struct {
	name   string
	value  float64
	labels Labels
}

func (f *fakeBackend) SetGauge(name string, value float64, labels Labels) {
	// For now we don’t need to record gauge calls.
	// Add tracking later if you write gauge tests.
}

func (f *fakeBackend) IncCounter(name string, delta float64, labels Labels) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callsCounters = append(f.callsCounters, counterCall{name, delta, labels})
}

func (f *fakeBackend) ObserveHistogram(name string, value float64, labels Labels) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callsHistograms = append(f.callsHistograms, histCall{name, value, labels})
}

func (f *fakeBackend) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushCount++
	return nil
}

func TestRecordStep_SuccessAndFailure(t *testing.T) {
	orig := backend
	defer func() { backend = orig }()

	fb := &fakeBackend{}
	backend = fb

	// Success case.
	RecordStep("jobA", "reader", nil, 2*time.Second)

	// Failure case.
	err := errors.New("boom")
	RecordStep("jobB", "loader", err, 1500*time.Millisecond)

	if len(fb.callsCounters) != 2 {
		t.Fatalf("expected 2 counter calls, got %d", len(fb.callsCounters))
	}
	if len(fb.callsHistograms) != 2 {
		t.Fatalf("expected 2 histogram calls, got %d", len(fb.callsHistograms))
	}

	// First call: success.
	cc0 := fb.callsCounters[0]
	if cc0.name != "etl_step_total" || cc0.delta != 1 {
		t.Fatalf("counter[0] = %#v; want name=etl_step_total, delta=1", cc0)
	}
	if got := cc0.labels["job"]; got != "jobA" {
		t.Fatalf("counter[0].labels[job]=%q; want %q", got, "jobA")
	}
	if got := cc0.labels["step"]; got != "reader" {
		t.Fatalf("counter[0].labels[step]=%q; want %q", got, "reader")
	}
	if got := cc0.labels["status"]; got != "success" {
		t.Fatalf("counter[0].labels[status]=%q; want %q", got, "success")
	}

	h0 := fb.callsHistograms[0]
	if h0.name != "etl_step_duration_seconds" {
		t.Fatalf("hist[0].name=%q; want etl_step_duration_seconds", h0.name)
	}
	if h0.value < 2.0-0.001 || h0.value > 2.0+0.001 {
		t.Fatalf("hist[0].value=%v; want ~2.0", h0.value)
	}

	// Second call: failure.
	cc1 := fb.callsCounters[1]
	if cc1.labels["job"] != "jobB" || cc1.labels["step"] != "loader" {
		t.Fatalf("counter[1] labels job/step = %v; want jobB/loader", cc1.labels)
	}
	if cc1.labels["status"] != "failure" {
		t.Fatalf("counter[1].labels[status]=%q; want %q", cc1.labels["status"], "failure")
	}

	h1 := fb.callsHistograms[1]
	if h1.value < 1.5-0.001 || h1.value > 1.5+0.001 {
		t.Fatalf("hist[1].value=%v; want ~1.5", h1.value)
	}
}

func TestRecordRowAndBatches(t *testing.T) {
	orig := backend
	defer func() { backend = orig }()

	fb := &fakeBackend{}
	backend = fb

	RecordRow("jobX", "processed", 3)
	RecordRow("jobX", "processed", 0) // should be ignored
	RecordRow("jobY", "inserted", 5)
	RecordBatches("jobZ", 2)

	if len(fb.callsCounters) != 3 {
		t.Fatalf("expected 3 counter calls, got %d", len(fb.callsCounters))
	}

	// 1) processed
	c0 := fb.callsCounters[0]
	if c0.name != "etl_records_total" || c0.delta != 3 {
		t.Fatalf("counter[0] = %#v; want name=etl_records_total, delta=3", c0)
	}
	if c0.labels["job"] != "jobX" || c0.labels["kind"] != "processed" {
		t.Fatalf("counter[0] labels = %v; want job=jobX, kind=processed", c0.labels)
	}

	// 2) inserted
	c1 := fb.callsCounters[1]
	if c1.name != "etl_records_total" || c1.delta != 5 {
		t.Fatalf("counter[1] = %#v; want name=etl_records_total, delta=5", c1)
	}
	if c1.labels["job"] != "jobY" || c1.labels["kind"] != "inserted" {
		t.Fatalf("counter[1] labels = %v; want job=jobY, kind=inserted", c1.labels)
	}

	// 3) batches
	c2 := fb.callsCounters[2]
	if c2.name != "etl_batches_total" || c2.delta != 2 {
		t.Fatalf("counter[2] = %#v; want name=etl_batches_total, delta=2", c2)
	}
	if c2.labels["job"] != "jobZ" {
		t.Fatalf("counter[2].labels[job]=%q; want %q", c2.labels["job"], "jobZ")
	}
}

func TestSetBackendAndFlush(t *testing.T) {
	orig := backend
	defer func() { backend = orig }()

	fb := &fakeBackend{}
	SetBackend(fb)

	if backend != fb {
		t.Fatal("SetBackend did not replace global backend")
	}

	if err := Flush(); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}
	if fb.flushCount != 1 {
		t.Fatalf("expected flushCount=1, got %d", fb.flushCount)
	}

	// SetBackend(nil) should not nil out the backend.
	SetBackend(nil)
	if backend != fb {
		t.Fatal("SetBackend(nil) should not change backend")
	}
}

func TestRecordHTTP(t *testing.T) {
	orig := backend
	defer func() { backend = orig }()

	tests := []struct {
		name           string
		job            string
		status         int
		err            error
		reqDur         time.Duration
		respDur        time.Duration
		bytes          int64
		wantCounters   []counterCall
		wantHistograms []histCall
	}{
		{
			name:    "success_records_all_metrics",
			job:     "jobA",
			status:  200,
			err:     nil,
			reqDur:  120 * time.Millisecond,
			respDur: 350 * time.Millisecond,
			bytes:   12345,
			wantCounters: []counterCall{
				{
					name:  "etl_http_requests_total",
					delta: 1,
					labels: Labels{
						"job":    "jobA",
						"status": "200",
					},
				},
			},
			wantHistograms: []histCall{
				{
					name:  "etl_http_request_duration_seconds",
					value: (120 * time.Millisecond).Seconds(),
					labels: Labels{
						"job":    "jobA",
						"status": "200",
					},
				},
				{
					name:  "etl_http_response_duration_seconds",
					value: (350 * time.Millisecond).Seconds(),
					labels: Labels{
						"job":    "jobA",
						"status": "200",
					},
				},
				{
					name:  "etl_http_download_bytes",
					value: float64(12345),
					labels: Labels{
						"job":    "jobA",
						"status": "200",
					},
				},
			},
		},
		{
			name:    "error_increments_error_counter",
			job:     "jobB",
			status:  429,
			err:     errors.New("rate limited"),
			reqDur:  10 * time.Millisecond,
			respDur: 20 * time.Millisecond,
			bytes:   0,
			wantCounters: []counterCall{
				{
					name:  "etl_http_requests_total",
					delta: 1,
					labels: Labels{
						"job":    "jobB",
						"status": "429",
					},
				},
				{
					name:  "etl_http_errors_total",
					delta: 1,
					labels: Labels{
						"job":    "jobB",
						"status": "429",
					},
				},
			},
			wantHistograms: []histCall{
				{
					name:  "etl_http_request_duration_seconds",
					value: (10 * time.Millisecond).Seconds(),
					labels: Labels{
						"job":    "jobB",
						"status": "429",
					},
				},
				{
					name:  "etl_http_response_duration_seconds",
					value: (20 * time.Millisecond).Seconds(),
					labels: Labels{
						"job":    "jobB",
						"status": "429",
					},
				},
				{
					name:  "etl_http_download_bytes",
					value: float64(0),
					labels: Labels{
						"job":    "jobB",
						"status": "429",
					},
				},
			},
		},
		{
			name:    "negative_values_are_ignored_except_request_counter",
			job:     "jobC",
			status:  0, // useful convention for transport errors
			err:     errors.New("dial tcp timeout"),
			reqDur:  -1 * time.Millisecond,
			respDur: -2 * time.Millisecond,
			bytes:   -1,
			wantCounters: []counterCall{
				{
					name:  "etl_http_requests_total",
					delta: 1,
					labels: Labels{
						"job":    "jobC",
						"status": "0",
					},
				},
				{
					name:  "etl_http_errors_total",
					delta: 1,
					labels: Labels{
						"job":    "jobC",
						"status": "0",
					},
				},
			},
			wantHistograms: nil, // all negative => ignored by RecordHTTP
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fb := &fakeBackend{}
			backend = fb

			RecordHTTP(tc.job, tc.status, tc.err, tc.reqDur, tc.respDur, tc.bytes)

			if len(fb.callsCounters) != len(tc.wantCounters) {
				t.Fatalf("expected %d counter calls, got %d: %#v", len(tc.wantCounters), len(fb.callsCounters), fb.callsCounters)
			}
			for i := range tc.wantCounters {
				got := fb.callsCounters[i]
				want := tc.wantCounters[i]

				if got.name != want.name || got.delta != want.delta {
					t.Fatalf("counter[%d]=%#v; want %#v", i, got, want)
				}
				// Label maps are order-independent; just compare values for required keys.
				for k, v := range want.labels {
					if got.labels[k] != v {
						t.Fatalf("counter[%d].labels[%q]=%q; want %q (all labels: %v)", i, k, got.labels[k], v, got.labels)
					}
				}
			}

			if len(fb.callsHistograms) != len(tc.wantHistograms) {
				t.Fatalf("expected %d histogram calls, got %d: %#v", len(tc.wantHistograms), len(fb.callsHistograms), fb.callsHistograms)
			}
			for i := range tc.wantHistograms {
				got := fb.callsHistograms[i]
				want := tc.wantHistograms[i]

				if got.name != want.name {
					t.Fatalf("hist[%d].name=%q; want %q", i, got.name, want.name)
				}
				// Allow tiny float error.
				if got.value < want.value-1e-9 || got.value > want.value+1e-9 {
					t.Fatalf("hist[%d].value=%v; want %v", i, got.value, want.value)
				}
				for k, v := range want.labels {
					if got.labels[k] != v {
						t.Fatalf("hist[%d].labels[%q]=%q; want %q (all labels: %v)", i, k, got.labels[k], v, got.labels)
					}
				}
			}
		})
	}
}
