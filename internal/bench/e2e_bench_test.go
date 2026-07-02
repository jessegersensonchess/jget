package bench

import (
	"context"
	"testing"

	"etl/internal/storage"
	"etl/internal/transformer"
)


// BenchmarkEndToEnd exercises the hot path of the streaming transform +
// batch loader pipeline in a simplified, in-memory setup.
//
// It focuses on:
//   - TransformLoopRows: string → typed coercion for realistic data
//   - LoadBatchesRows:   batching semantics feeding a fake COPY function
//
// The goal is to approximate real-world throughput without involving I/O or
// actual database drivers.
// Run with:
//
//	go test -run=^$ -bench ^BenchmarkEndToEnd$ -cpuprofile cpu.out -memprofile mem.out -count=1
func BenchmarkEndToEnd(b *testing.B) {
	ctx := context.Background()

	// Columns mimic a small, realistic subset of the production schema.
	cols := []string{"pcv", "typ", "stav", "platnost_od", "aktualni"}

	// Coercion spec: pcv=int, typ/stav=text, platnost_od=date, aktualni=bool.
	spec := transformer.BuildCoerceSpecFromTypes(
		map[string]string{
			"pcv":         "int",
			"typ":         "text",
			"stav":        "text",
			"platnost_od": "date",
			"aktualni":    "bool",
		},
		"02.01.2006", // CZ-style date layout used in the real pipeline
		nil,
		nil,
	)

	// Producer: generate b.N pooled rows with realistic string values.
	// We close 'in' once all rows are produced.
	in := make(chan *transformer.Row, 8192)
	go func() {
		defer close(in)
		for i := 0; i < b.N; i++ {
			r := transformer.GetRow(len(cols))
			r.V[0] = "123456"        // pcv (int)
			r.V[1] = "E - Evidenční" // typ (text)
			r.V[2] = "Nezjištěno"    // stav (text)
			r.V[3] = "07.10.2011"    // platnost_od (date)
			r.V[4] = "True"          // aktualni (bool)
			in <- r
		}
	}()

	// Transform stage: coerce in place on pooled rows.
	// We pass a nil onReject callback so that this benchmark measures strictly
	// the transform cost, without additional logging or aggregation overhead.
	out := make(chan *transformer.Row, 8192)
	go func() {
		transformer.TransformLoopRows(ctx, cols, in, out, spec, nil)
		close(out)
	}()

	// Fake copyFn that just reports how many rows it would have inserted.
	// This isolates batch-building and iteration costs from actual I/O.
	copyFn := func(ctx context.Context, columns []string, rows [][]any) (int64, error) {
		return int64(len(rows)), nil
	}

	b.ResetTimer()
	n, err := storage.LoadBatchesRows(ctx, cols, toRowLike(out), 4096, copyFn)
	b.StopTimer()

	if err != nil {
		b.Fatalf("LoadBatchesRows: %v", err)
	}

	// n is the total number of rows "inserted"; we just ensure the value is
	// used so the compiler does not optimize away the benchmark path.
	_ = n
}


// toRowLike adapts transformer.Row to storage.RowLike without allocation.
func toRowLike(in <-chan *transformer.Row) <-chan *storage.RowLike {
	out := make(chan *storage.RowLike, 8192)
	go func() {
		defer close(out)
		for r := range in {
			row := r
			out <- &storage.RowLike{
				V:        row.V,
				FreeFunc: row.Free,
			}
		}
	}()
	return out
}
