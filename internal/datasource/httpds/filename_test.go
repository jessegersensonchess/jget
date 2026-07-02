package httpds

import "testing"

func TestHashString_Stable(t *testing.T) {
	t.Parallel()

	const input = "https://example.com/path?x=1&y=2"
	got1 := HashString(input)
	got2 := HashString(input)

	if got1 == "" {
		t.Fatalf("HashString returned empty string")
	}
	if got1 != got2 {
		t.Fatalf("HashString(%q) not stable: %q vs %q", input, got1, got2)
	}
}

func TestSafeFilenameFromURL_UsesQuery(t *testing.T) {
	t.Parallel()

	raw := "https://example.com/search?q=hello+world&lang=en"
	got := SafeFilenameFromURL(raw)

	if got == "" {
		t.Fatalf("SafeFilenameFromURL(%q) returned empty filename", raw)
	}
	// We expect alphanumeric/underscore only, so just sanity check:
	for _, r := range got {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_') {
			t.Fatalf("SafeFilenameFromURL(%q) produced invalid char %q in %q", raw, r, got)
		}
	}
}

func TestSafeFilenameFromURL_FallsBackOnInvalidURL(t *testing.T) {
	t.Parallel()

	raw := ":// not a url"
	got := SafeFilenameFromURL(raw)

	if got == "" {
		t.Fatalf("SafeFilenameFromURL(%q) returned empty string for invalid URL", raw)
	}

	// For an invalid URL, we expect it to be a hash; at minimum, ensure it
	// differs from the raw input.
	if got == raw {
		t.Fatalf("SafeFilenameFromURL(%q) returned raw input, want hash-like string", raw)
	}
}

func TestSafeFilenameFromURL_FallsBackOnEmptyQuery(t *testing.T) {
	t.Parallel()

	raw := "https://example.com/noquery"
	got := SafeFilenameFromURL(raw)

	if got == "" {
		t.Fatalf("SafeFilenameFromURL(%q) returned empty string", raw)
	}
	// The output should not look like "noquery" (we're hashing entire URL).
	if got == "noquery" {
		t.Fatalf("SafeFilenameFromURL(%q) unexpectedly used path instead of hash", raw)
	}
}
