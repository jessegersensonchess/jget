// Package httpds implements a small HTTP datasource with built-in retry/backoff
// and optional TLS verification skipping. It is intended to be used by the ETL
// pipeline as a source of bytes (e.g., for CSV/XML downloads).
//
// Design goals:
//
//   - Keep a tiny, explicit API (Get, Post, Put, Do).
//   - Handle transient failures with exponential backoff.
//   - Allow skipping TLS verification when talking to endpoints with invalid
//     certificates (e.g., internal test endpoints).
//   - Respect context cancellation during requests and backoff waits.
//   - Be easy to test by injecting a custom RoundTripper and sleep function.
package httpds

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"
)

// Config configures the HTTP datasource client.
//
// Zero values are given sensible defaults:
//   - Timeout:        30s
//   - MaxRetries:     3
//   - InitialBackoff: 200ms
//   - MaxBackoff:     5s
type Config struct {
	// Timeout is the per-request timeout applied at the http.Client level.
	Timeout time.Duration

	// MaxRetries is the number of retry attempts after the initial request.
	// MaxRetries=0 means "no retries" (only the initial attempt).
	MaxRetries int

	// InitialBackoff is the base backoff duration for the first retry.
	// Each subsequent retry doubles the previous backoff up to MaxBackoff.
	InitialBackoff time.Duration

	// MaxBackoff caps the exponential backoff duration.
	MaxBackoff time.Duration

	// InsecureSkipVerify controls whether TLS certificate verification is
	// disabled. This is useful for talking to servers with self-signed or
	// otherwise invalid certificates, but should be used with care.
	InsecureSkipVerify bool

	// BaseHeaders are headers added to every request. Callers can supply
	// additional headers per request; those take precedence.
	BaseHeaders http.Header

	// Transport is an optional custom RoundTripper. When nil, a default
	// *http.Transport is constructed based on the TLS and timeout settings.
	Transport http.RoundTripper
}

// Client wraps an http.Client with retry and backoff behavior.
type Client struct {
	httpClient     *http.Client
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	baseHeaders    http.Header

	// sleep is injectable to make tests fast and deterministic.
	sleep func(time.Duration)
}

// NewClient constructs a Client from Config, applying defaults for zero values.
func NewClient(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 200 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Second
	}

	transport := cfg.Transport
	if transport == nil {
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // explicitly configurable
			},
		}
	}

	hdr := http.Header{}
	for k, vs := range cfg.BaseHeaders {
		for _, v := range vs {
			hdr.Add(k, v)
		}
	}

	return &Client{
		httpClient: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		maxRetries:     cfg.MaxRetries,
		initialBackoff: cfg.InitialBackoff,
		maxBackoff:     cfg.MaxBackoff,
		baseHeaders:    hdr,
		sleep:          time.Sleep,
	}
}

// Do sends an HTTP request with the given method, URL, and optional body,
// applying retry and backoff on transient errors. The body is supplied as a
// byte slice so that it can be safely re-sent on retry.
//
// The returned *http.Response has a non-nil Body which the caller must close.
// On error, either no response was obtained or the last response encountered
// a non-retryable status.
func (c *Client) Do(
	ctx context.Context,
	method, url string,
	body []byte,
	headers http.Header,
) (*http.Response, error) {
	if method == "" {
		return nil, fmt.Errorf("httpds: method must not be empty")
	}
	if url == "" {
		return nil, fmt.Errorf("httpds: url must not be empty")
	}

	attempts := c.maxRetries + 1
	var lastErr error

	for attempt := 0; attempt < attempts; attempt++ {
		// Respect context cancellation before each attempt.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		reqBody := bytes.NewReader(body)
		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return nil, fmt.Errorf("httpds: build request: %w", err)
		}

		// Apply base headers, then per-request headers (which override).
		for k, vs := range c.baseHeaders {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		for k, vs := range headers {
			for _, v := range vs {
				req.Header.Set(k, v)
			}
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Network or transport-level error. Treat as retryable.
			lastErr = err
		} else {
			// We have a response; decide whether to retry based on status.
			if !isRetryableStatus(resp.StatusCode) {
				return resp, nil
			}
			// Retryable status: close body and fall through to backoff.
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("httpds: retryable status %d from %s %s", resp.StatusCode, method, url)
		}

		// If this was the last allowed attempt, return the last error.
		if attempt+1 >= attempts {
			return nil, lastErr
		}

		// Wait with backoff before retrying.
		backoff := backoffDuration(c.initialBackoff, attempt, c.maxBackoff)
		// Sleep is injected for tests; respect context cancellation around it.
		if err := sleepWithContext(ctx, c.sleep, backoff); err != nil {
			return nil, err
		}
	}

	// Should never reach here because of the attempts loop logic.
	return nil, lastErr
}

// Get is a convenience wrapper over Do for HTTP GET. The caller must close
// the response body.
func (c *Client) Get(ctx context.Context, url string, headers http.Header) (*http.Response, error) {
	return c.Do(ctx, http.MethodGet, url, nil, headers)
}

// Post is a convenience wrapper over Do for HTTP POST. The caller must close
// the response body.
func (c *Client) Post(ctx context.Context, url string, body []byte, headers http.Header) (*http.Response, error) {
	return c.Do(ctx, http.MethodPost, url, body, headers)
}

// Put is a convenience wrapper over Do for HTTP PUT. The caller must close
// the response body.
func (c *Client) Put(ctx context.Context, url string, body []byte, headers http.Header) (*http.Response, error) {
	return c.Do(ctx, http.MethodPut, url, body, headers)
}

// isRetryableStatus reports whether the given HTTP status code should trigger
// a retry. This is intentionally conservative: 5xx and 429 are treated as
// transient; everything else is considered final.
func isRetryableStatus(code int) bool {
	if code == http.StatusTooManyRequests {
		return true
	}
	return code >= 500 && code <= 599
}

// backoffDuration returns the exponential backoff duration for the given
// attempt number (0-based retry index), clamped to max.
func backoffDuration(initial time.Duration, attempt int, max time.Duration) time.Duration {
	if attempt <= 0 {
		if initial > max {
			return max
		}
		return initial
	}
	// exponential: initial * 2^attempt
	d := initial << attempt
	if d > max {
		return max
	}
	return d
}

// sleepWithContext sleeps for d using the provided sleep function,
// but aborts early if ctx is canceled.
func sleepWithContext(ctx context.Context, sleep func(time.Duration), d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		// Use injected sleep just to keep behavior uniform; for most callers
		// this will be time.Sleep and timer will already have waited.
		sleep(0)
		return nil
	}
}
