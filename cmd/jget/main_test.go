package main

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"etl/internal/metrics"
)

// testBackend is a minimal metrics backend used in tests.
//
// It is safe for concurrent use in these tests because it performs no mutation.
type testBackend struct{}

func (testBackend) IncCounter(name string, delta float64, labels metrics.Labels)       {}
func (testBackend) ObserveHistogram(name string, value float64, labels metrics.Labels) {}
func (testBackend) Flush() error                                                       { return nil }
func (testBackend) Close() error                                                       { return nil }
func (testBackend) SetGauge(name string, value float64, labels metrics.Labels)         {}

// TestParseFlags validates flag parsing and basic validation.
//
// When to use:
//   - Ensure argument handling remains stable as flags evolve.
//
// Edge cases:
//   - Missing required flags should error.
//   - Invalid values should error.
//   - Defaults should be set when flags are absent.
func TestParseFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		wantErr   string
		wantField func(t *testing.T, cfg runConfig)
	}{
		{
			name:    "missing_url_file",
			args:    []string{},
			wantErr: "missing required -i",
		},
		{
			name:    "invalid_workers",
			args:    []string{"-i", "x", "-n", "0"},
			wantErr: "-n must be > 0",
		},
		{
			name:    "invalid_max_attempts",
			args:    []string{"-i", "x", "-max_attempts", "0"},
			wantErr: "-max_attempts must be > 0",
		},
		{
			name:    "invalid_max_conns",
			args:    []string{"-i", "x", "-max_conns_per_host", "-1"},
			wantErr: "-max_conns_per_host must be >= 0",
		},
		{
			name: "defaults",
			args: []string{"-i", "x"},
			wantField: func(t *testing.T, cfg runConfig) {
				if cfg.Workers != 4 {
					t.Fatalf("Workers=%d, want 4", cfg.Workers)
				}
				if cfg.MaxConnsPerHost != 32 {
					t.Fatalf("MaxConnsPerHost=%d, want 32", cfg.MaxConnsPerHost)
				}
			},
		},
		{
			name: "custom_max_conns",
			args: []string{"-i", "x", "-max_conns_per_host", "7"},
			wantField: func(t *testing.T, cfg runConfig) {
				if cfg.MaxConnsPerHost != 7 {
					t.Fatalf("MaxConnsPerHost=%d, want 7", cfg.MaxConnsPerHost)
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := parseFlags(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseFlags() err=%v, want contains %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFlags() err=%v, want nil", err)
			}
			if tc.wantField != nil {
				tc.wantField(t, cfg)
			}
		})
	}
}

// TestRun_ConfigErrors verifies run() returns exit code 2 for configuration issues.
//
// When to use:
//   - Keep user-visible behavior stable (exit codes are part of CLI contract).
func TestRun_ConfigErrors(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer

	code := run(context.Background(), []string{}, deps{
		Stdout: &out,
		Stderr: &errOut,
		BackendFactory: func(ctx context.Context, jobName string, tags []string, flushEvery time.Duration) (backendCloser, error) {
			return testBackend{}, nil
		},
		Now:   time.Now,
		Sleep: func(time.Duration) {},
	})

	if code != 2 {
		t.Fatalf("run()=%d, want 2", code)
	}
	if got := errOut.String(); !strings.Contains(got, "missing required -i") {
		t.Fatalf("stderr=%q, want contains %q", got, "missing required -i")
	}
}

// TestDoAttempt_SuccessWritesFile verifies doAttempt streams the body to disk on 2xx.
//
// When to use:
//   - Validate the HTTP read/write happy path without running the worker pool.
//
// Edge cases:
//   - Ensures File is set when write succeeds.
//   - Ensures DownloadSz matches response body size.
func TestDoAttempt_SuccessWritesFile(t *testing.T) {
	t.Parallel()

	metrics.SetBackend(testBackend{})

	const payload = "hello world"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	outPath := filepath.Join(dir, "out")

	client := newHTTPClient(5*time.Second, 8)
	rec := doAttempt(context.Background(), client, srv.URL, 1, outPath, false)

	if rec.StatusCode != 200 {
		t.Fatalf("StatusCode=%d, want 200; rec=%+v", rec.StatusCode, rec)
	}
	if rec.DownloadSz != int64(len(payload)) {
		t.Fatalf("DownloadSz=%d, want %d", rec.DownloadSz, len(payload))
	}
	if rec.File != outPath {
		t.Fatalf("File=%q, want %q", rec.File, outPath)
	}
	if rec.RequestMs < 0 || rec.ResponseMs < 0 || rec.DurationMs < 0 {
		t.Fatalf("timings must be set; got RequestMs=%d ResponseMs=%d DurationMs=%d", rec.RequestMs, rec.ResponseMs, rec.DurationMs)
	}

	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) err=%v", outPath, err)
	}
	if string(b) != payload {
		t.Fatalf("file content=%q, want %q", string(b), payload)
	}
}

// TestProcessURL_404IsSuccess verifies HTTP 404 is treated as a success.
//
// When to use:
//   - Ensure crawler behavior matches expectations for missing resources.
//
// Edge cases:
//   - No file should be written for 404.
//   - The function should return true.
func TestProcessURL_404IsSuccess(t *testing.T) {
	t.Parallel()

	metrics.SetBackend(testBackend{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()

	cfg := workerConfig{
		jobName:       "test",
		outDir:        dir,
		maxAttempts:   3,
		baseBackoff:   0,
		maxBackoff:    0,
		sleepBefore:   0,
		jitterMax:     0,
		logHeaders429: false,
	}

	client := newHTTPClient(5*time.Second, 8)
	logCh := make(chan logRecord, 10)

	// Deterministic RNG; jitterMax=0 means no randomness affects behavior.
	rng := randForTest()
	s := newSleeper(rng, 0, 0, func(time.Duration) {})

	ok := processURL(context.Background(), client, srv.URL, cfg, s, logCh)
	if !ok {
		t.Fatalf("processURL()=false, want true")
	}

	outPath := filepath.Join(dir, hashString(srv.URL))
	if _, err := os.Stat(outPath); err == nil {
		t.Fatalf("unexpected file written for 404: %s", outPath)
	}
}

func randForTest() *rand.Rand {
	return rand.New(rand.NewSource(1))
}
