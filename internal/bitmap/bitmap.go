// internal/bitmap/bitmap.go

// Package bitmap provides a simple, memory-efficient bitmap implementation
// for representing sets of non-negative integer IDs. It is useful in ETL
// pipelines for fast membership checks (e.g., "is this ID in the allowlist?").
package bitmap

// Bitmap represents a bitset backed by a slice of uint64 words.
// Each bit corresponds to a non-negative integer ID.
type Bitmap struct {
	data []uint64
}

// New allocates a bitmap that can store bits for IDs in the range [0, maxID].
//
// If maxID <= 0, no backing storage is allocated and the bitmap behaves as
// an empty set.
func New(maxID int) *Bitmap {
	if maxID <= 0 {
		return &Bitmap{data: nil}
	}
	nWords := (maxID + 63) / 64 // enough 64-bit words to cover [0, maxID]
	return &Bitmap{
		data: make([]uint64, nWords),
	}
}

// Add sets the bit corresponding to id in the bitmap. Negative ids are ignored.
//
// If id exceeds the capacity implied by New(maxID), the call is a no-op.
func (b *Bitmap) Add(id int) {
	if id < 0 {
		return
	}
	word := id / 64
	if word < 0 || word >= len(b.data) {
		// Out of range for this bitmap; ignore.
		return
	}
	bit := uint(id % 64)
	b.data[word] |= 1 << bit
}

// Has reports whether the bit corresponding to id is set in the bitmap.
// Negative ids always return false.
func (b *Bitmap) Has(id int) bool {
	if id < 0 {
		return false
	}
	word := id / 64
	if word < 0 || word >= len(b.data) {
		return false
	}
	bit := uint(id % 64)
	return (b.data[word] & (1 << bit)) != 0
}
