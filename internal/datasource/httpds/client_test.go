// internal/datasource/httpds/client_test.go
//
// These tests exercise the behavior of the HTTP datasource client wrapper,
// focusing on:
//   - Default configuration and TLS settings.
//   - Retry and backoff behavior on transient failures.
//   - Handling of non-retryable statuses.
//   - Use of custom transports.
//   - Context-aware sleep behavior.
//
// The goal is to keep the client predictable and safe for ETL pipelines that
// rely on HTTP as a data source.

package httpds

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewClient_Defaults verifies that NewClient applies sensible defaults
// and correctly sets TLS behavior when no custom Transport is supplied.
func TestNewClient_Defaults(t *testing.T) {
	t.Parallel()

	cfg := Config{
		InsecureSkipVerify: true,
	}
	c := NewClient(cfg)

	// Ensure a timeout is set; a zero timeout would be dangerous in ETL code.
	if c.httpClient.Timeout <= 0 {
		t.Fatalf("expected non-zero timeout, got %v", c.httpClient.Timeout)
	}
	if c.maxRetries != 0 {
		t.Fatalf("expected default maxRetries=0, got %d", c.maxRetries)
	}
	if c.initialBackoff <= 0 {
		t.Fatalf("expected default initialBackoff > 0, got %v", c.initialBackoff)
	}
	if c.maxBackoff <= 0 {
		t.Fatalf("expected default maxBackoff > 0, got %v", c.maxBackoff)
	}

	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c.httpClient.Transport)
	}
	if transport.TLSClientConfig == nil {
		t.Fatalf("expected TLSClientConfig to be non-nil")
	}
	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("expected InsecureSkipVerify=true when configured")
	}
}

// TestDo_Success_NoRetry verifies that a successful 200 response returns
// immediately without retries, even when MaxRetries > 0.
func TestDo_Success_NoRetry(t *testing.T) {
	t.Parallel()

	var hits int32

	// Test server that always returns 200 OK.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(Config{
		MaxRetries:     3,               // allow retries but they should not be used
		Timeout:        2 * time.Second, // small timeout for test
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
	})

	// Override sleep to avoid real delays during tests.
	c.sleep = func(time.Duration) {}

	ctx := context.Background()
	resp, err := c.Get(ctx, srv.URL, nil)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected exactly 1 request, got %d", got)
	}
}

// TestDo_RetryOn5xxThenSuccess verifies that the client retries on a 5xx
// status and eventually returns the successful response once the server
// recovers.
//
// The sequence is:
//   - First two requests: 500
//   - Third request: 200
//
// This ensures both retry and backoff logic are exercised.
func TestDo_RetryOn5xxThenSuccess(t *testing.T) {
	t.Parallel()

	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(Config{
		MaxRetries:     3,
		Timeout:        2 * time.Second,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})

	// Record sleep durations without actually sleeping, to keep tests fast.
	var sleeps []time.Duration
	c.sleep = func(d time.Duration) {
		sleeps = append(sleeps, d)
	}

	ctx := context.Background()
	resp, err := c.Get(ctx, srv.URL, nil)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	// We expect 3 total attempts: 2 failures + 1 success.
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 attempts (2x500 + 1x200), got %d", got)
	}
	// Ensure we actually invoked backoff at least once.
	if len(sleeps) == 0 {
		t.Fatalf("expected at least one backoff sleep, got none")
	}
}

// TestDo_StopsAfterMaxRetries verifies that the client stops after the maximum
// number of retries and returns an error when all responses remain retryable
// (e.g., all 503).
func TestDo_StopsAfterMaxRetries(t *testing.T) {
	t.Parallel()

	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient(Config{
		MaxRetries:     2, // initial + 2 retries = 3 attempts total
		Timeout:        2 * time.Second,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})
	c.sleep = func(time.Duration) {}

	ctx := context.Background()
	resp, err := c.Get(ctx, srv.URL, nil)
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("expected error after exhausting retries, got nil")
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 attempts (1 initial + 2 retries), got %d", got)
	}
}

// TestDo_NonRetryableStatus verifies that non-retryable status codes (e.g. 400)
// do not trigger retries: the client should return immediately.
func TestDo_NonRetryableStatus(t *testing.T) {
	t.Parallel()

	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest) // 400 is not retryable
	}))
	defer srv.Close()

	c := NewClient(Config{
		MaxRetries:     5, // retries allowed but should not be used here
		Timeout:        2 * time.Second,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})
	c.sleep = func(time.Duration) {}

	ctx := context.Background()
	resp, err := c.Get(ctx, srv.URL, nil)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 attempt for non-retryable status, got %d", got)
	}
}

// TestBackoffDuration verifies the exponential backoff logic with clamping
// at a maximum duration.
func TestBackoffDuration(t *testing.T) {
	t.Parallel()

	type testCase struct {
		initial time.Duration
		attempt int
		max     time.Duration
		want    time.Duration
	}
	tests := []testCase{
		{
			initial: 100 * time.Millisecond,
			attempt: 0,
			max:     1 * time.Second,
			want:    100 * time.Millisecond,
		},
		{
			initial: 100 * time.Millisecond,
			attempt: 1,
			max:     1 * time.Second,
			want:    200 * time.Millisecond,
		},
		{
			initial: 100 * time.Millisecond,
			attempt: 2,
			max:     1 * time.Second,
			want:    400 * time.Millisecond,
		},
		{
			initial: 600 * time.Millisecond,
			attempt: 1,
			max:     1 * time.Second,
			want:    1 * time.Second, // clamped to max
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.initial.String()+"/attempt="+itoa(tt.attempt), func(t *testing.T) {
			t.Parallel()

			got := backoffDuration(tt.initial, tt.attempt, tt.max)
			if got != tt.want {
				t.Fatalf(
					"backoffDuration(%v, %d, %v) = %v, want %v",
					tt.initial, tt.attempt, tt.max, got, tt.want,
				)
			}
		})
	}
}

// TestIsRetryableStatus verifies that 5xx and 429 are considered retryable,
// while common non-retryable statuses are not.
func TestIsRetryableStatus(t *testing.T) {
	t.Parallel()

	retryable := []int{429, 500, 503}
	nonRetryable := []int{200, 400, 404}

	for _, code := range retryable {
		code := code
		t.Run("retryable/"+itoa(code), func(t *testing.T) {
			t.Parallel()
			if !isRetryableStatus(code) {
				t.Fatalf("expected status %d to be retryable", code)
			}
		})
	}
	for _, code := range nonRetryable {
		code := code
		t.Run("non-retryable/"+itoa(code), func(t *testing.T) {
			t.Parallel()
			if isRetryableStatus(code) {
				t.Fatalf("expected status %d to be non-retryable", code)
			}
		})
	}
}

// TestCustomTransport ensures that when a custom Transport is supplied, it is
// used as-is and TLS settings from Config are not applied on top of it.
func TestCustomTransport(t *testing.T) {
	t.Parallel()

	customTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
		},
	}
	cfg := Config{
		Transport:          customTransport,
		InsecureSkipVerify: true, // should be ignored because we provided Transport
	}
	c := NewClient(cfg)

	if !reflect.DeepEqual(c.httpClient.Transport, customTransport) {
		t.Fatalf("expected custom transport to be used")
	}
	tp, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c.httpClient.Transport)
	}
	if tp.TLSClientConfig == nil || tp.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("expected TLSClientConfig.InsecureSkipVerify=false when custom transport is provided")
	}
}

// TestSleepWithContextCancellation verifies that sleepWithContext returns early
// when the context is canceled, rather than waiting for the full duration.
func TestSleepWithContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := sleepWithContext(ctx, func(time.Duration) {}, 100*time.Millisecond)
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context error, got %v", err)
	}
}

// itoa is a small helper for test names; avoids importing strconv for this.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [32]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
