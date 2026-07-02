package extracthtml

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadMappingFile_HappyPath verifies we can parse mappings and enforce
// the "must have at least one mapping" invariant.
func TestLoadMappingFile_HappyPath(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	p := filepath.Join(tmp, "m.json")

	if err := os.WriteFile(p, []byte(`{"mappings":[{"selector":"h1","extract":"text","json_path":"x"}]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	mf, err := LoadMappingFile(p)
	if err != nil {
		t.Fatalf("LoadMappingFile: %v", err)
	}
	if len(mf.Mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(mf.Mappings))
	}
}

// TestLoadMappingFile_NoMappings verifies we reject empty mapping files.
// This protects downstream code from silent "no-op" extractions.
func TestLoadMappingFile_NoMappings(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	p := filepath.Join(tmp, "m.json")

	if err := os.WriteFile(p, []byte(`{"mappings":[]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadMappingFile(p)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
