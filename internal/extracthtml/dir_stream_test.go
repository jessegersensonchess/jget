package extracthtml

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestStreamFromDir_SingleObject verifies:
//   - stable filename ordering
//   - one JSON object per file
//   - source_file is injected
func TestStreamFromDir_SingleObject(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	// Note: We create files out of order to ensure sorting is correct.
	if err := os.WriteFile(filepath.Join(tmp, "b.html"), []byte(`<h1>B</h1>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "a.html"), []byte(`<h1>A</h1>`), 0o600); err != nil {
		t.Fatal(err)
	}

	mf := &MappingFile{
		Mappings: []Mapping{
			{Selector: "h1", Extract: "text", JSONPath: "title"},
		},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := StreamFromDir(&buf, tmp, mf, enc); err != nil {
		t.Fatalf("StreamFromDir: %v", err)
	}

	var arr []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &arr); err != nil {
		t.Fatalf("invalid json: %v; out=%s", err, buf.String())
	}

	if len(arr) != 2 {
		t.Fatalf("want 2 records got %d", len(arr))
	}

	// Sorted order should be a.html, then b.html.
	if arr[0]["title"] != "A" || arr[0]["source_file"] != "a.html" {
		t.Fatalf("unexpected first record: %#v", arr[0])
	}
	if arr[1]["title"] != "B" || arr[1]["source_file"] != "b.html" {
		t.Fatalf("unexpected second record: %#v", arr[1])
	}
}

// TestStreamFromDir_RecordMode verifies record mode can emit multiple rows per file.
//
// This is common when each file is a listing page with many result rows.
func TestStreamFromDir_RecordMode(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmp, "x.html"), []byte(`
		<div class="rec"><span class="n">A</span></div>
		<div class="rec"><span class="n">B</span></div>
	`), 0o600); err != nil {
		t.Fatal(err)
	}

	mf := &MappingFile{
		RecordSelector: ".rec",
		Mappings: []Mapping{
			{Selector: ".n", Extract: "text", JSONPath: "name"},
		},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := StreamFromDir(&buf, tmp, mf, enc); err != nil {
		t.Fatalf("StreamFromDir: %v", err)
	}

	var arr []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &arr); err != nil {
		t.Fatalf("invalid json: %v; out=%s", err, buf.String())
	}

	if len(arr) != 2 {
		t.Fatalf("want 2 records got %d", len(arr))
	}
	if arr[0]["name"] != "A" || arr[1]["name"] != "B" {
		t.Fatalf("unexpected names: %#v", arr)
	}
	if arr[0]["source_file"] != "x.html" || arr[1]["source_file"] != "x.html" {
		t.Fatalf("expected source_file x.html for all records: %#v", arr)
	}
}
