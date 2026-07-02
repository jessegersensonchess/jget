// Generic, reusable URL -> filename + hashing helpers
// internal/datasource/httpds/filename.go

package httpds

import (
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"regexp"
)

// filenameCleaner replaces sequences of non-alphanumeric characters with "_".
var filenameCleaner = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// HashString returns a stable SHA1 hex digest of s. It is useful for generating
// deterministic identifiers or filenames when a natural key is not available.
func HashString(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// SafeFilenameFromURL derives a filesystem-safe filename from a raw URL string.
// It prefers to use the URL's raw query string (since that often encodes
// the "interesting" parameters) but falls back to hashing the entire URL if:
//
//   - the URL cannot be parsed, or
//   - the cleaned query string is empty.
//
// Non-alphanumeric characters in the chosen query string are replaced by
// underscores, and multiple such characters are collapsed into a single "_".
func SafeFilenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return HashString(rawURL)
	}

	clean := filenameCleaner.ReplaceAllString(u.RawQuery, "_")
	if clean == "" {
		return HashString(rawURL)
	}

	return clean
}
