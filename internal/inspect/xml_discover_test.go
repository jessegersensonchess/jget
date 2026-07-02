package inspect

import (
	"encoding/json"
	"strings"
	"testing"

	xmlparser "etl/internal/parser/xml"
)

func TestDiscover_Basic(t *testing.T) {
	xml := `<Root><Rec><A>1</A><B x="y">z</B></Rec><Rec><A>2</A></Rec></Root>`
	rep, err := Discover(strings.NewReader(xml), "Rec")
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalRecords != 2 {
		t.Fatalf("records=%d want 2", rep.TotalRecords)
	}
	if rep.Paths["A"].TotalCount != 2 {
		t.Fatalf("A TotalCount=%d want 2", rep.Paths["A"].TotalCount)
	}
	if rep.Paths["B"].AttrExamples["x"]["y"] == 0 {
		t.Fatalf("expected attr example x=y")
	}
	ps := SortedPaths(rep)
	if len(ps) == 0 || ps[0] == "" {
		t.Fatalf("sorted paths empty")
	}
}

func TestDiscover_TruncatedInput(t *testing.T) {
	// The second record is cut off.
	xml := `<Root><Rec><A>1</A></Rec><Rec><A>2</A>`
	rep, err := Discover(strings.NewReader(xml), "Rec")
	if err != nil {
		t.Fatal(err)
	}
	// Only one fully closed record should be counted.
	if rep.TotalRecords != 1 {
		t.Fatalf("records=%d want 1", rep.TotalRecords)
	}
	if rep.Paths["A"].TotalCount != 1 {
		t.Fatalf("A TotalCount=%d want 1", rep.Paths["A"].TotalCount)
	}
}

func TestStarterConfigFrom_RoundtripJSON(t *testing.T) {
	xml := `<Root><X><A>1</A><A>2</A><B>k</B></X><X><B>m</B></X></Root>`
	rep, err := Discover(strings.NewReader(xml), "X")
	if err != nil {
		t.Fatal(err)
	}
	cfg := StarterConfigFrom(rep)
	if cfg.RecordTag != "X" {
		t.Fatalf("record_tag=%q", cfg.RecordTag)
	}
	// A should be list (appears twice per record), B field.
	if cfg.Lists["A"] != "A" {
		t.Fatalf("A not in lists")
	}
	if cfg.Fields["B"] != "B" {
		t.Fatalf("B not in fields")
	}
	// Marshal and parse back using xmlparser to ensure compatibility.
	b, err := xmlparser.MarshalConfigJSON(cfg, true)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := xmlparser.ParseConfigJSON(b)
	if err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if back.RecordTag != "X" {
		t.Fatalf("roundtrip mismatch: %q", back.RecordTag)
	}
}

func TestGuessRecordTag(t *testing.T) {
	xml := `<Root><Item><A/></Item><Item><A/></Item><Other/></Root>`
	tag, err := GuessRecordTag(strings.NewReader(xml))
	if err != nil {
		t.Fatal(err)
	}
	if tag != "Item" {
		t.Fatalf("guess=%q want Item", tag)
	}
}

func TestStarterConfig_JSONShape(t *testing.T) {
	xml := `<R><Rec><A>1</A></Rec></R>`
	rep, _ := Discover(strings.NewReader(xml), "Rec")
	cfg := StarterConfigFrom(rep)
	// Ensure it marshals to JSON fine.
	if _, err := json.Marshal(cfg); err != nil {
		t.Fatalf("marshal err=%v", err)
	}
}
