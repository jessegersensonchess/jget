package file

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalOpen covers success, missing file, and pre-canceled context.
// Table-driven to make behavior clear and extensible.
func TestLocalOpen(t *testing.T) {
	t.Parallel()

	type tc struct {
		name            string
		prepare         func(t *testing.T) string // returns path to open
		makeCtx         func(t *testing.T) context.Context
		wantErrIs       error  // checked via errors.Is
		wantErrContains string // substring expected in error message
		wantContent     string // if non-empty, verifies read content on success
	}

	cases := []tc{
		{
			name: "success_reads_content",
			prepare: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				p := filepath.Join(dir, "data.txt")
				const payload = "hello\nworld"
				if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
					t.Fatalf("write test file: %v", err)
				}
				return p
			},
			makeCtx:     func(t *testing.T) context.Context { return context.Background() },
			wantContent: "hello\nworld",
		},
		{
			name: "missing_file_errors_with_wrapping",
			prepare: func(t *testing.T) string {
				t.Helper()
				return filepath.Join(t.TempDir(), "missing.txt")
			},
			makeCtx:         func(t *testing.T) context.Context { return context.Background() },
			wantErrIs:       os.ErrNotExist,
			wantErrContains: "open ",
		},
		{
			name: "pre_canceled_context_short_circuits",
			prepare: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				p := filepath.Join(dir, "data.txt")
				if err := os.WriteFile(p, []byte("ignored"), 0o644); err != nil {
					t.Fatalf("write test file: %v", err)
				}
				return p
			},
			makeCtx: func(t *testing.T) context.Context {
				t.Helper()
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErrIs: context.Canceled,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			path := c.prepare(t)
			ctx := c.makeCtx(t)

			rc, err := NewLocal(path).Open(ctx)

			// Error expectations.
			if c.wantErrIs != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", c.wantErrIs)
				}
				if !errors.Is(err, c.wantErrIs) {
					t.Fatalf("errors.Is(%v, %v) = false", err, c.wantErrIs)
				}
				if c.wantErrContains != "" && !strings.Contains(err.Error(), c.wantErrContains) {
					t.Fatalf("error %q does not contain substring %q", err, c.wantErrContains)
				}
				// Ensure no ReadCloser was returned on error.
				if rc != nil {
					_ = rc.Close()
					t.Fatalf("got non-nil ReadCloser on error: %T", rc)
				}
				return
			}

			// Success expectations.
			if err != nil {
				t.Fatalf("Open() unexpected error: %v", err)
			}
			defer rc.Close()

			if c.wantContent != "" {
				got, rerr := io.ReadAll(rc)
				if rerr != nil {
					t.Fatalf("reading: %v", rerr)
				}
				if string(got) != c.wantContent {
					t.Fatalf("content mismatch: got %q, want %q", string(got), c.wantContent)
				}
			}
		})
	}
}

// BenchmarkLocalOpen_Success measures the steady-state cost of opening a small file.
// We open and immediately close to isolate os.Open + descriptor work.
func BenchmarkLocalOpen_Success(b *testing.B) {
	dir := b.TempDir()
	p := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(p, []byte("payload"), 0o644); err != nil {
		b.Fatalf("write test file: %v", err)
	}

	src := NewLocal(p)
	ctx := context.Background()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rc, err := src.Open(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if err := rc.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLocalOpen_Missing measures the cost of failing fast on missing files.
// Useful to ensure the error path doesn't allocate excessively.
func BenchmarkLocalOpen_Missing(b *testing.B) {
	p := filepath.Join(b.TempDir(), "missing.txt")
	src := NewLocal(p)
	ctx := context.Background()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rc, err := src.Open(ctx)
		if err == nil {
			rc.Close()
			b.Fatal("expected error, got nil")
		}
	}
}

// BenchmarkLocalOpen_PreCanceled measures short-circuit cost when the context
// is already canceled at call time (the common cancellation case).
func BenchmarkLocalOpen_PreCanceled(b *testing.B) {
	dir := b.TempDir()
	p := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(p, []byte("payload"), 0o644); err != nil {
		b.Fatalf("write test file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	src := NewLocal(p)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rc, err := src.Open(ctx)
		if err == nil {
			rc.Close()
			b.Fatal("expected context error, got nil")
		}
	}
}
