package httpds

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
)

// FetchFirstBytes retrieves up to n bytes from the given URL using HTTP GET.
//
// It:
//   - Adds a Range header ("bytes=0-(n-1)") as an optimization
//   - Uses a client-side LimitedReader so the result is capped even when
//     the server ignores the Range header.
//
// The returned slice length is <= n.
func (c *Client) FetchFirstBytes(ctx context.Context, url string, n int) ([]byte, error) {
	if n <= 0 {
		return nil, fmt.Errorf("httpds: n must be > 0")
	}

	h := make(http.Header)
	h.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))

	resp, err := c.Do(ctx, http.MethodGet, url, nil, h)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Regardless of 206 or 200, only read up to n bytes.
	lr := &io.LimitedReader{R: resp.Body, N: int64(n)}

	var buf bytes.Buffer
	_, err = buf.ReadFrom(lr)
	if err != nil && err != io.EOF {
		return nil, err
	}

	return buf.Bytes(), nil
}
