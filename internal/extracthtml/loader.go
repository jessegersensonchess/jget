package extracthtml

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Input describes where HTML should come from.
type Input struct {
	// URL, if provided, is fetched via HTTP GET.
	URL string

	// Stdin is used when URL is empty. If nil, stdin reads as empty.
	Stdin io.Reader
}

// Loader fetches or reads HTML with a consistent timeout policy.
type Loader struct {
	client  *http.Client
	timeout time.Duration
}

// NewLoader creates a Loader. If client is nil, http.DefaultClient is used.
func NewLoader(client *http.Client, timeout time.Duration) *Loader {
	if client == nil {
		client = http.DefaultClient
	}
	return &Loader{
		client:  client,
		timeout: timeout,
	}
}

// Load returns the HTML source for either stdin (when input.URL is empty)
// or a fetched URL.
//
// On non-2xx HTTP responses, Load returns an error that includes the status
// code and up to 4KB of the response body for debugging.
func (l *Loader) Load(ctx context.Context, input Input) (string, error) {
	if strings.TrimSpace(input.URL) == "" {
		if input.Stdin == nil {
			return "", nil
		}
		b, err := io.ReadAll(input.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}

	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URL, nil)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", "extract-html/1.0")

	resp, err := l.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("http status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(b), nil
}
