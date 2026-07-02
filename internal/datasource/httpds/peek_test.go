// These tests exercise the FetchFirstBytes method, which retrieves at most N
// bytes from a given URL, using Range when possible but falling back to a
// client-side limit if the server ignores Range.
//
// The behavior is important for ETL components that "peek" at the beginning of
// a file (e.g., to detect delimiter, format, or record structure) without
// downloading the entire object.

package httpds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestFetchFirstBytes_LimitsToN verifies that FetchFirstBytes never returns
// more than N bytes, even when the server ignores the Range header and returns
// a full body.
func TestFetchFirstBytes_LimitsToN(t *testing.T) {
	t.Parallel()

	const body = "hello world"
	const n = 5

	// Server that always returns the full body, ignoring Range.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client := NewClient(Config{
		MaxRetries:     0, // do not retry in this test
		Timeout:        2 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
	})
	client.sleep = func(time.Duration) {} // avoid real sleeps

	ctx := context.Background()
	got, err := client.FetchFirstBytes(ctx, srv.URL, n)
	if err != nil {
		t.Fatalf("FetchFirstBytes error: %v", err)
	}

	if len(got) != n {
		t.Fatalf("expected %d bytes, got %d (%q)", n, len(got), string(got))
	}
	if string(got) != body[:n] {
		t.Fatalf("unexpected content: got %q, want %q", string(got), body[:n])
	}
}

// TestFetchFirstBytes_SendsRangeHeader verifies that FetchFirstBytes sets the
// correct Range header when N > 0.
//
// Note: the server in this test still ignores Range; the goal is only to
// observe what header is being sent.
func TestFetchFirstBytes_SendsRangeHeader(t *testing.T) {
	t.Parallel()

	const n = 5
	var sawRange string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRange = r.Header.Get("Range")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("abcdefg"))
	}))
	defer srv.Close()

	client := NewClient(Config{
		MaxRetries:     0,
		Timeout:        2 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
	})
	client.sleep = func(time.Duration) {}

	ctx := context.Background()
	got, err := client.FetchFirstBytes(ctx, srv.URL, n)
	if err != nil {
		t.Fatalf("FetchFirstBytes error: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expected %d bytes, got %d", n, len(got))
	}
	if sawRange != "bytes=0-4" {
		t.Fatalf("expected Range header %q, got %q", "bytes=0-4", sawRange)
	}
}

// TestFetchFirstBytes_InvalidN verifies that n <= 0 is rejected with an error.
// This guards against misuse of the API by callers.
func TestFetchFirstBytes_InvalidN(t *testing.T) {
	t.Parallel()

	client := NewClient(Config{})
	client.sleep = func(time.Duration) {}

	ctx := context.Background()
	if _, err := client.FetchFirstBytes(ctx, "http://example.com", 0); err == nil {
		t.Fatalf("expected error for n <= 0, got nil")
	}
}

// TestFetchFirstBytes_ContextCanceled verifies that FetchFirstBytes respects
// context cancellation and returns promptly without performing work when the
// context is already canceled.
//
// We do not attempt to assert precisely whether the server handler was invoked,
// but we ensure the function returns a context error.
func TestFetchFirstBytes_ContextCanceled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If this handler is called, it will be a no-op; correctness is judged
		// by the returned error, not by server side effects.
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(Config{
		MaxRetries:     1,
		Timeout:        2 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
	})
	client.sleep = func(time.Duration) {}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.FetchFirstBytes(ctx, srv.URL, 10)
	if err == nil {
		t.Fatalf("expected error due to canceled context, got nil")
	}
}
