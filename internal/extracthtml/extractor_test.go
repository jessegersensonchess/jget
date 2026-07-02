package extracthtml

import (
	"reflect"
	"regexp"
	"testing"
)

// TestExtractOneHTML_JSEmail verifies js_email extraction is wired to the
// project’s canonical decoder (internal/emailparser).
//
// This test uses a minimal script that matches the required "var a='...'" pattern.
func TestExtractOneHTML_JSEmail(t *testing.T) {
	t.Parallel()

	html := `<script>var a='me&#64;example.com';</script>`
	mappings := []Mapping{
		{Selector: "script", Extract: "js_email", JSONPath: "email"},
	}

	got, err := ExtractOneHTML(html, mappings)
	if err != nil {
		t.Fatalf("ExtractOneHTML: %v", err)
	}
	if got["email"] != "me@example.com" {
		t.Fatalf("expected %q, got %#v", "me@example.com", got["email"])
	}
}

/////////////////////////////////////////////////////////

// TestExtractRecordsHTML verifies record mode extracts one object per record.
//
// This test exists specifically to cover ExtractRecordsHTML, which parses HTML
// and delegates to record extraction logic.
func TestExtractRecordsHTML(t *testing.T) {
	t.Parallel()

	html := `
		<div class="rec"><span class="name">A</span></div>
		<div class="rec"><span class="name">B</span></div>
	`

	recs, err := ExtractRecordsHTML(html, ".rec", []Mapping{
		{Selector: ".name", Extract: "text", JSONPath: "name"},
	})
	if err != nil {
		t.Fatalf("ExtractRecordsHTML: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0]["name"] != "A" || recs[1]["name"] != "B" {
		t.Fatalf("unexpected records: %#v", recs)
	}
}

// TestParseSelection_Attr verifies the "attr" extraction path, including trimming.
//
// This test exists because the attr switch case was previously uncovered.
func TestParseSelection_Attr(t *testing.T) {
	t.Parallel()

	html := `<a class="x" href=" https://example.com/path ">link</a>`
	got, err := ExtractOneHTML(html, []Mapping{
		{Selector: "a.x", Extract: "attr", Attr: "href", JSONPath: "href"},
	})
	if err != nil {
		t.Fatalf("ExtractOneHTML: %v", err)
	}
	if got["href"] != "https://example.com/path" {
		t.Fatalf("expected trimmed href, got %#v", got["href"])
	}
}

// TestApplyRegexFilter exercises all significant branches in applyRegexFilter.
//
// Coverage goals:
//   - nil regex: passthrough
//   - no match: empty output
//   - match with capture groups: return group 1
//   - match without capture groups: return full match
func TestApplyRegexFilter(t *testing.T) {
	t.Parallel()

	// Case 1: nil regex should return input unchanged.
	if got := applyRegexFilter("abc", nil); got != "abc" {
		t.Fatalf("nil regex: expected %q, got %q", "abc", got)
	}

	// Case 2: non-nil regex but no match should return "".
	reNoMatch := regexp.MustCompile(`\d+`)
	if got := applyRegexFilter("abc", reNoMatch); got != "" {
		t.Fatalf("no match: expected empty string, got %q", got)
	}

	// Case 3: capture group should return group 1.
	reCapture := regexp.MustCompile(`id=(\d+)`)
	if got := applyRegexFilter("id=123", reCapture); got != "123" {
		t.Fatalf("capture: expected %q, got %q", "123", got)
	}

	// Case 4: no capture groups should return full match.
	reFull := regexp.MustCompile(`\d+`)
	if got := applyRegexFilter("x=123", reFull); got != "123" {
		t.Fatalf("full match: expected %q, got %q", "123", got)
	}
}

// TestExtractOneHTML_All verifies Mapping.All collects all matches into []string.
//
// Keeping this test here ensures parseSelection's "All" branch stays covered.
func TestExtractOneHTML_All(t *testing.T) {
	t.Parallel()

	html := `<ul><li>A</li><li> B </li><li></li></ul>`
	mappings := []Mapping{
		{Selector: "li", Extract: "text", JSONPath: "items", All: true},
	}
	got, err := ExtractOneHTML(html, mappings)
	if err != nil {
		t.Fatalf("ExtractOneHTML: %v", err)
	}

	want := []string{"A", "B"}
	if !reflect.DeepEqual(got["items"], want) {
		t.Fatalf("items: want %#v got %#v", want, got["items"])
	}
}
