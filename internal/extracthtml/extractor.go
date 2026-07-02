package extracthtml

import (
	"fmt"
	"regexp"
	"strings"

	"etl/internal/emailparser"

	"github.com/PuerkitoBio/goquery"
)

// ExtractOneHTML parses the given HTML string and applies mappings relative to
// the document root.
//
// This is the "single object" extraction mode: mappings are evaluated against
// the full document and returned as a single JSON-ready map.
//
// Missing selectors are not treated as errors; they simply produce no output.
func ExtractOneHTML(html string, mappings []Mapping) (map[string]any, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	return parseSelection(doc.Selection, mappings)
}

// ExtractRecordsHTML parses the given HTML string and extracts one JSON-ready
// map per record container matched by recordSelector.
//
// This is the "record mode": each element matched by recordSelector becomes an
// independent extraction root, and mappings are evaluated relative to that root.
//
// The returned slice preserves DOM order.
func ExtractRecordsHTML(html, recordSelector string, mappings []Mapping) ([]map[string]any, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	return extractRecords(doc, recordSelector, mappings), nil
}

// extractRecords iterates all record containers matched by recordSelector and
// extracts one map per container.
//
// This helper is resilient by design: if extraction for a given record returns
// an error (e.g., invalid regex in mappings), that record is skipped so the
// pipeline can continue processing other records.
func extractRecords(doc *goquery.Document, recordSelector string, mappings []Mapping) []map[string]any {
	var records []map[string]any

	doc.Find(recordSelector).Each(func(_ int, rec *goquery.Selection) {
		obj, err := parseSelection(rec, mappings)
		if err != nil {
			// Skip a bad record rather than failing the entire page.
			return
		}
		if len(obj) > 0 {
			records = append(records, obj)
		}
	})

	return records
}

// parseSelection applies all mappings relative to root and returns a JSON-ready map.
//
// Semantics:
//   - If Mapping.All is true, all selector matches are collected into []string.
//   - Otherwise, only the first match is extracted.
//   - If Mapping.Match is set, it is treated as a regular expression:
//   - If the regex contains capturing groups, group 1 is used as output.
//   - Otherwise, the full match is used.
//     If the regex does not match, the field is omitted.
//
// Resilience:
// Missing selectors are not treated as errors; they simply produce no output.
func parseSelection(root *goquery.Selection, mappings []Mapping) (map[string]any, error) {
	output := make(map[string]any)

	for _, mapping := range mappings {
		re, err := compileOptionalRegex(mapping.Match, mapping.JSONPath)
		if err != nil {
			return nil, err
		}

		// extractOne converts a matched node into the extracted string value.
		// It returns "" to represent "no value" for this mapping at this node.
		extractOne := func(sel *goquery.Selection) string {
			switch mapping.Extract {
			case "text":
				return strings.TrimSpace(sel.Text())

			case "attr":
				if mapping.Attr == "" {
					return ""
				}
				if val, ok := sel.Attr(mapping.Attr); ok {
					return strings.TrimSpace(val)
				}
				return ""

			case "js_email":
				// js_email decodes obfuscated email addresses found in inline scripts.
				return strings.TrimSpace(emailparser.DecodeEmailFromScript(sel.Text()))

			default:
				// Unknown extraction modes intentionally produce no value.
				return ""
			}
		}

		if mapping.All {
			var vals []string
			root.Find(mapping.Selector).Each(func(_ int, sel *goquery.Selection) {
				v := extractOne(sel)
				v = applyRegexFilter(v, re)
				if v == "" {
					return
				}
				vals = append(vals, v)
			})
			if len(vals) > 0 {
				output[mapping.JSONPath] = vals
			}
			continue
		}

		sel := root.Find(mapping.Selector).First()
		if sel.Length() == 0 {
			continue
		}

		v := extractOne(sel)
		v = applyRegexFilter(v, re)
		if v == "" {
			continue
		}
		output[mapping.JSONPath] = v
	}

	return output, nil
}

// compileOptionalRegex compiles pattern into a regexp.Regexp.
//
// If pattern is empty, it returns (nil, nil).
// If pattern is invalid, it returns an error annotated with jsonPath to make
// debugging mapping configurations straightforward.
func compileOptionalRegex(pattern, jsonPath string) (*regexp.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex for json_path=%q: %w", jsonPath, err)
	}
	return re, nil
}

// applyRegexFilter applies an optional regex post-processing step to value.
//
// Behavior:
//   - If re is nil, it returns value unchanged.
//   - If re does not match, it returns "" (caller should omit the field).
//   - If re matches and contains capture groups, group 1 is returned.
//   - If re matches with no capture groups, the full match is returned.
func applyRegexFilter(value string, re *regexp.Regexp) string {
	if value == "" || re == nil {
		return value
	}

	sm := re.FindStringSubmatch(value)
	if len(sm) == 0 {
		return ""
	}
	if len(sm) > 1 {
		return sm[1]
	}
	return sm[0]
}
