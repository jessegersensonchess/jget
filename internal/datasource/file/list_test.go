package file

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTempFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestReadList_Basic(t *testing.T) {
	t.Parallel()

	content := `
# comment line
https://example.com
   # indented comment
https://example.org

   https://example.net
`
	path := writeTempFile(t, content)

	got, err := ReadList(path)
	if err != nil {
		t.Fatalf("ReadList error: %v", err)
	}

	want := []string{
		"https://example.com",
		"https://example.org",
		"https://example.net",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadList(%q) = %#v, want %#v", path, got, want)
	}
}

func TestReadList_EmptyFile(t *testing.T) {
	t.Parallel()

	path := writeTempFile(t, "")
	got, err := ReadList(path)
	if err != nil {
		t.Fatalf("ReadList error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %#v", got)
	}
}

func TestReadList_FileNotFound(t *testing.T) {
	t.Parallel()

	_, err := ReadList("does-not-exist-12345.txt")
	if err == nil {
		t.Fatalf("expected error for missing file, got nil")
	}
}
