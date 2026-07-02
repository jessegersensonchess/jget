// Package file implements a local filesystem-backed data source.
package file

import (
	"context"
	"fmt"
	"io"
	"os"
)

// Local is a filesystem data source that opens files from the local disk.
type Local struct{ path string }

// NewLocal returns a new Local data source bound to the provided filesystem
// path. The returned value is safe for concurrent use by multiple goroutines
// as long as the underlying path location is valid for concurrent reads.
func NewLocal(path string) *Local { return &Local{path: path} }

// Open opens the configured path for reading and returns an io.ReadCloser.
//
// Behavior:
//   - If the context is already canceled or its deadline exceeded at the time
//     of the call, Open returns the context error immediately without touching
//     the filesystem.
//   - Otherwise, Open attempts to open the underlying file and returns the
//     resulting *os.File as an io.ReadCloser.
//   - Any filesystem error is wrapped with the path for context, while still
//     permitting errors.Is/As checks by callers (e.g., errors.Is(err, os.ErrNotExist)).
func (l *Local) Open(ctx context.Context) (io.ReadCloser, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	f, err := os.Open(l.path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", l.path, err)
	}
	return f, nil
}
