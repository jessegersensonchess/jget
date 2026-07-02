package extracthtml

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// StreamFromDir streams a single JSON array to w, emitting one object per file,
// and adding "source_file" to each emitted object.
//
// Behavior matches the original program:
//   - stable ordering by filename
//   - unreadable/unparseable files are skipped
//   - record mode may emit multiple objects per file (one per record)
func StreamFromDir(w io.Writer, dir string, mf *MappingFile, enc *json.Encoder) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	if _, err := io.WriteString(w, "["); err != nil {
		return fmt.Errorf("write [: %w", err)
	}

	first := true
	emit := func(obj map[string]any) error {
		if len(obj) == 0 {
			return nil
		}
		if !first {
			if _, err := io.WriteString(w, ","); err != nil {
				return fmt.Errorf("write comma: %w", err)
			}
		}
		first = false
		if err := enc.Encode(obj); err != nil {
			return fmt.Errorf("encode record: %w", err)
		}
		return nil
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		full := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(b)))
		if err != nil {
			continue
		}

		if strings.TrimSpace(mf.RecordSelector) != "" {
			recs := extractRecords(doc, mf.RecordSelector, mf.Mappings)
			for _, r := range recs {
				if len(r) == 0 {
					continue
				}
				r["source_file"] = e.Name()
				if err := emit(r); err != nil {
					return err
				}
			}
			continue
		}

		obj, err := parseSelection(doc.Selection, mf.Mappings)
		if err != nil {
			continue
		}
		if len(obj) == 0 {
			continue
		}
		obj["source_file"] = e.Name()
		if err := emit(obj); err != nil {
			return err
		}
	}

	if _, err := io.WriteString(w, "]"); err != nil {
		return fmt.Errorf("write ]: %w", err)
	}
	return nil
}
