package extracthtml

import (
	"bytes"
	"net/url"
	"testing"
)

// TestParseCountAny verifies we can parse common "human formatted" counts.
//
// This is a surprisingly important helper because many sites use spaces
// as thousand separators, or embed counts in parentheses.
func TestParseCountAny(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in     any
		want   int
		wantOK bool
	}{
		{"(1 096 ...)", 1096, true},
		{"51", 51, true},
		{"no digits", 0, false},
		{"", 0, false},
	}

	for _, tt := range tests {
		got, ok, err := ParseCountAny(tt.in)
		if err != nil {
			t.Fatalf("ParseCountAny(%v): %v", tt.in, err)
		}
		if ok != tt.wantOK || got != tt.want {
			t.Fatalf("ParseCountAny(%v): want (%d,%v) got (%d,%v)", tt.in, tt.want, tt.wantOK, got, ok)
		}
	}
}

// TestResolveHref verifies relative links become absolute when base is provided.
// This is required to generate correct pagination URLs from listing pages.
func TestResolveHref(t *testing.T) {
	t.Parallel()

	base, _ := url.Parse("https://example.com/root/page")
	got := ResolveHref(base, "/cat")
	if got != "https://example.com/cat" {
		t.Fatalf("unexpected resolved url: %q", got)
	}
}

// /////////////////////////////////
// TestPrintPagingURLs_RecordMode verifies record mode prints pages per record.
//
// This test covers:
//   - record extraction flow (RecordSelector + mappings)
//   - relative href resolution against listURL
//   - page count math (perPage=25)
func TestPrintPagingURLs_RecordMode(t *testing.T) {
	t.Parallel()

	html := `
		<div class="rec">
			<a class="href" href="/cat">Category</a>
			<span class="count">(51)</span>
		</div>
	`

	mf := &MappingFile{
		RecordSelector: ".rec",
		Mappings: []Mapping{
			{Selector: "a.href", Extract: "attr", Attr: "href", JSONPath: "href"},
			{Selector: "span.count", Extract: "text", JSONPath: "count"},
		},
	}

	var buf bytes.Buffer
	listURL := "https://example.com/list"

	if err := PrintPagingURLs(&buf, listURL, html, mf); err != nil {
		t.Fatalf("PrintPagingURLs: %v", err)
	}

	// 51 items => 3 pages at 25/page.
	want := "https://example.com/cat\nhttps://example.com/cat/2\nhttps://example.com/cat/3\n"
	if buf.String() != want {
		t.Fatalf("unexpected output:\nwant=%q\ngot=%q", want, buf.String())
	}
}

// TestPrintPagingURLs_RecordMode_CountZero verifies we suppress output when count==0.
//
// This matches the requirement: if a row's count is 0, print nothing for that row.
func TestPrintPagingURLs_RecordMode_CountZero(t *testing.T) {
	t.Parallel()

	html := `
		<div class="rec">
			<a class="href" href="/cat">Category</a>
			<span class="count">(0)</span>
		</div>
	`

	mf := &MappingFile{
		RecordSelector: ".rec",
		Mappings: []Mapping{
			{Selector: "a.href", Extract: "attr", Attr: "href", JSONPath: "href"},
			{Selector: "span.count", Extract: "text", JSONPath: "count"},
		},
	}

	var buf bytes.Buffer
	if err := PrintPagingURLs(&buf, "https://example.com/list", html, mf); err != nil {
		t.Fatalf("PrintPagingURLs: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

// TestPrintPagingURLs_SingleMode verifies non-record mode prints pages for listURL.
//
// This test covers the "single count" path, which uses JSONPath "count" from
// parseSelection on the document root.
func TestPrintPagingURLs_SingleMode(t *testing.T) {
	t.Parallel()

	html := `<span id="count">(26)</span>`

	mf := &MappingFile{
		Mappings: []Mapping{
			{Selector: "#count", Extract: "text", JSONPath: "count"},
		},
	}

	var buf bytes.Buffer
	listURL := "https://example.com/list/"

	if err := PrintPagingURLs(&buf, listURL, html, mf); err != nil {
		t.Fatalf("PrintPagingURLs: %v", err)
	}

	// 26 items => 2 pages.
	want := "https://example.com/list\nhttps://example.com/list/2\n"
	if buf.String() != want {
		t.Fatalf("unexpected output:\nwant=%q\ngot=%q", want, buf.String())
	}
}
