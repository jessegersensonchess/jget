package datasource

import (
	"context"
	"io"
)

type Source interface {
	Open(ctx context.Context) (io.ReadCloser, error)
}
