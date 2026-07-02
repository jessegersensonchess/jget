package extracthtml

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"
)

// TestLoader_Stdin verifies stdin input is read and returned as string.
//
// This is the most common mode when piping HTML from another program.
func TestLoader_Stdin(t *testing.T) {
	t.Parallel()

	l := NewLoader(http.DefaultClient, 1*time.Second)
	html, err := l.Load(context.Background(), Input{
		Stdin: bytes.NewBufferString("<p>x</p>"),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if html != "<p>x</p>" {
		t.Fatalf("unexpected html: %q", html)
	}
}

// TestLoader_URL_Non2xx verifies we include status code and a body snippet.
// This dramatically improves debuggability when scraping.
// func TestLoader_URL_Non2xx(t *testing.T) {
// 	t.Parallel()
//
// 	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
// 		http.Error(w, "nope", http.StatusForbidden)
// 	}))
// 	t.Cleanup(srv.Close)
//
// 	l := NewLoader(&http.Client{Timeout: 2 * time.Second}, 2*time.Second)
// 	_, err := l.Load(context.Background(), Input{URL: srv.URL})
// 	if err == nil {
// 		t.Fatalf("expected error, got nil")
// 	}
// 	msg := err.Error()
// 	if !strings.Contains(msg, "http status 403") || !strings.Contains(msg, "nope") {
// 		t.Fatalf("unexpected error: %v", err)
// 	}
// }
