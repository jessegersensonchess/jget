package bitmap

import "testing"

// TestNew verifies that New allocates the correct number of underlying
// uint64 "words" for different maxID values.
//
// The key property in the current implementation: we compute the number of
// words as (maxID + 63) / 64. That is a ceiling-like function that ensures
// we have enough bits for indices up to (but not strictly guaranteed to be)
// maxID, depending on how maxID relates to 64. The tests below lock in that
// formula so changes are intentional and explicit.
func TestNew(t *testing.T) {
	t.Parallel()

	// Table-driven tests keep related scenarios together and make it easy to
	// extend cases later without changing the test logic.
	tests := []struct {
		name    string
		maxID   int
		wantLen int
	}{
		{
			name:    "maxID <= 0 yields empty backing slice",
			maxID:   0,
			wantLen: 0,
		},
		{
			name:    "small positive maxID",
			maxID:   1,
			wantLen: 1, // IDs [0..1] fit into 1 word under (maxID+63)/64
		},
		{
			name:    "exact 63 boundary fits in one word",
			maxID:   63,
			wantLen: 1, // (63+63)/64 = 1
		},
		{
			name:  "64 still results in one word with current formula",
			maxID: 64,
			// With nWords = (maxID + 63) / 64, 64 maps to (64+63)/64 = 1.
			// This means the highest reliably representable bit index is 63
			// for this configuration. The test documents that behavior rather
			// than asserting a semantic of "always covers maxID exactly".
			wantLen: 1,
		},
		{
			name:    "large maxID sanity check",
			maxID:   150000000,
			wantLen: (150000000 + 63) / 64,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bm := New(tt.maxID)
			if got := len(bm.data); got != tt.wantLen {
				t.Fatalf("New(%d) data length = %d, want %d", tt.maxID, got, tt.wantLen)
			}
		})
	}
}

// TestAddAndHas verifies the basic semantics of Add and Has:
//   - IDs we Add should later be reported as present.
//   - IDs we do not Add should be reported as absent.
//   - Negative IDs and out-of-range IDs should be safely ignored.
func TestAddAndHas(t *testing.T) {
	t.Parallel()

	// Use a modest maxID that creates multiple words so we exercise more than
	// one slot in the backing slice.
	bm := New(200)

	// Before adding anything, Has should always return false.
	if bm.Has(0) || bm.Has(50) || bm.Has(199) {
		t.Fatalf("bitmap should start empty")
	}

	// Add a mix of IDs:
	//   - negative (should be ignored)
	//   - boundary IDs around word transitions (63, 64)
	//   - an in-range high value (199)
	//   - a clearly out-of-range value (1000) that should be ignored.
	bm.Add(-1)   // should be ignored
	bm.Add(0)    // first bit in first word
	bm.Add(63)   // last bit in first word
	bm.Add(64)   // first bit in second word (since we allocated enough for 200)
	bm.Add(199)  // in-range, last valid ID
	bm.Add(1000) // out of range, must not panic or mutate

	// Define expectations for a set of IDs that cover the above scenarios.
	tests := []struct {
		id   int
		want bool
	}{
		{-1, false},   // negative IDs are never present
		{0, true},     // explicitly added
		{1, false},    // not added
		{63, true},    // explicitly added
		{64, true},    // explicitly added (crosses word boundary)
		{199, true},   // explicitly added
		{200, false},  // in-range but not added
		{1000, false}, // out-of-range; Add should be a no-op
	}

	for _, tt := range tests {
		tt := tt
		t.Run(
			// Use a descriptive subtest name to make failures easier to interpret.
			funcNameForID(tt.id),
			func(t *testing.T) {
				t.Parallel()

				got := bm.Has(tt.id)
				if got != tt.want {
					t.Fatalf("Has(%d) = %v, want %v", tt.id, got, tt.want)
				}
			},
		)
	}
}

// funcNameForID returns a short, descriptive name for a subtest given an ID.
// This is primarily to improve readability in test output when a case fails.
func funcNameForID(id int) string {
	return "id=" + itoa(id)
}

// itoa is a small, local helper to avoid pulling in strconv for test naming.
// It is not performance-critical and is intentionally simple.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [32]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// BenchmarkHas measures the performance of Has lookups when the bitmap
// contains a reasonable number of set bits. This is useful as a sanity check
// for changes that might impact tight loops in ETL code.
func BenchmarkHas(b *testing.B) {
	// Create a bitmap with room for 1,000,000 IDs.
	bm := New(1_000_000)

	// Set some bits in a sparse but deterministic pattern.
	for i := 0; i < 10000; i += 3 {
		bm.Add(i)
	}

	// Reset the timer to exclude setup time from the benchmark.
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Exercise Has across the full range; this simulates a typical lookup.
		_ = bm.Has(i % 1_000_000)
	}
}
