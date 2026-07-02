package extracthtml

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

const perPage = 25

var reDigitGroups = regexp.MustCompile(`\d+`)

// PrintPagingURLs prints paginated category URLs inferred from extracted counts.
//
// Inputs:
//   - listURL: the URL the HTML was fetched from (used for resolving relative hrefs)
//   - html:    the HTML of the listing page
//   - mf:      mappings configuration; if mf.RecordSelector is set, paging is
//     computed per record using "href" and "count".
//
// Output:
//   - For each category (record mode) or for the listing page (non-record mode),
//     prints one URL per line.
//   - If count is 0 or missing, prints nothing for that item.
func PrintPagingURLs(w io.Writer, listURL string, html string, mf *MappingFile) error {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return fmt.Errorf("parse html %s: %w", listURL, err)
	}

	base, _ := url.Parse(listURL)

	if strings.TrimSpace(mf.RecordSelector) != "" {
		return printPagingURLsRecordMode(w, base, doc, mf)
	}
	return printPagingURLsSingleMode(w, listURL, doc, mf)
}

// printPagingURLsRecordMode prints paging URLs for each extracted record.
//
// Each record must produce:
//   - "href": category URL (relative or absolute)
//   - "count": total item count (string with digits)
func printPagingURLsRecordMode(w io.Writer, base *url.URL, doc *goquery.Document, mf *MappingFile) error {
	records := extractRecords(doc, mf.RecordSelector, mf.Mappings)

	for _, r := range records {
		href, _ := r["href"].(string)
		href = strings.TrimSpace(href)
		if href == "" {
			continue
		}

		count, ok, err := ParseCountAny(r["count"])
		if err != nil {
			continue
		}
		if !ok || count == 0 {
			continue
		}

		absHref := ResolveHref(base, href)
		if err := printPages(w, absHref, count); err != nil {
			return err
		}
	}

	return nil
}

// printPagingURLsSingleMode prints paging URLs for a single listing page.
//
// It expects mf mappings to produce a single field "count".
func printPagingURLsSingleMode(w io.Writer, listURL string, doc *goquery.Document, mf *MappingFile) error {
	obj, err := parseSelection(doc.Selection, mf.Mappings)
	if err != nil {
		return err
	}

	count, ok, err := ParseCountAny(obj["count"])
	if err != nil {
		return err
	}
	if !ok || count == 0 {
		return nil
	}

	trimmed := strings.TrimRight(listURL, "/")
	return printPages(w, trimmed, count)
}

// printPages prints the first page URL and any subsequent "/N" pages based on count.
//
// Example (count=51, perPage=25):
//
//	baseURL
//	baseURL/2
//	baseURL/3
func printPages(w io.Writer, baseURL string, count int) error {
	totalPages := (count + perPage - 1) / perPage

	if _, err := fmt.Fprintln(w, baseURL); err != nil {
		return err
	}
	for p := 2; p <= totalPages; p++ {
		if _, err := fmt.Fprintf(w, "%s/%d\n", strings.TrimRight(baseURL, "/"), p); err != nil {
			return err
		}
	}
	return nil
}

// ResolveHref resolves href against base, returning an absolute URL string.
// If href is invalid, it is returned unchanged.
func ResolveHref(base *url.URL, href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if base == nil {
		return u.String()
	}
	return base.ResolveReference(u).String()
}

// ParseCountAny extracts an integer count from v.
//
// It accepts inputs like "(1 096 ...)" by joining digit groups into "1096".
// It returns ok=false when v contains no digits.
func ParseCountAny(v any) (count int, ok bool, err error) {
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}

	parts := reDigitGroups.FindAllString(s, -1)
	if len(parts) == 0 {
		return 0, false, nil
	}

	numStr := strings.Join(parts, "")
	n, convErr := strconv.Atoi(numStr)
	if convErr != nil {
		return 0, false, convErr
	}
	return n, true, nil
}
