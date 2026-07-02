// Package file contains helpers for reading local files as datasources,
// such as simple line-based lists of URLs, IDs, or other tokens.
package file

import (
	"bufio"
	"os"
	"strings"
)

// ReadList reads a text file line by line and returns a slice of strings
// containing non-empty, non-comment lines.
//
// Lines that are empty or start with '#' (after trimming leading/trailing
// whitespace) are skipped. This makes it convenient to maintain list files
// with comments and blank separators.
//
// The order of lines is preserved. On I/O error, a non-nil error is returned.
func ReadList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
