// Package inspect implements streaming/truncation-tolerant XML discovery utilities.
// It inventories all element paths relative to a configured record tag, collects
// example texts and attribute value frequencies, and can synthesize a starter
// JSON config for the XML parser.
//
// Design goals:
//   - Tolerant to truncated inputs (e.g., first N bytes of a large file).
//     Only fully-closed records are counted. The decoder runs with Strict=false,
//     and "unexpected EOF" is treated as end-of-stream.
//   - Stdlib-only. No external dependencies beyond internal/parser/xml.
package inspect

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"

	xmlparser "etl/internal/parser/xml"
)

// PathAgg aggregates statistics for a relative element path found under the record tag.
type PathAgg struct {
	TotalCount   int                       `json:"total_count"`
	RecordsWith  int                       `json:"records_with"`
	MaxPerRecord int                       `json:"max_per_record"`
	ExampleTexts []string                  `json:"example_texts,omitempty"`
	AttrExamples map[string]map[string]int `json:"attr_examples,omitempty"` // attr -> value -> count
}

type DiscoverReport struct {
	RecordTag    string             `json:"record_tag"`
	TotalRecords int                `json:"total_records"`
	Paths        map[string]PathAgg `json:"paths"`
}

// Discover scans r, tolerant to truncation, and inventories all element paths under <recordTag>.
// It only merges data for fully-closed records; truncated tails do not affect counts.
func Discover(r io.Reader, recordTag string) (DiscoverReport, error) {
	if strings.TrimSpace(recordTag) == "" {
		return DiscoverReport{}, fmt.Errorf("recordTag is required")
	}
	dec := xml.NewDecoder(r)
	dec.Strict = false

	rep := DiscoverReport{
		RecordTag: recordTag,
		Paths:     map[string]PathAgg{},
	}

	type frame struct {
		name  string
		attrs []xml.Attr
		text  []byte
	}
	// Per-record aggregation to ensure truncated records don't leak into totals.
	type recAgg struct {
		count      int
		exTexts    []string
		attrCounts map[string]map[string]int // attr -> val -> count
	}

	var (
		inRecord  bool
		relStack  []string
		nodeStack []frame
		perRec    map[string]*recAgg // path -> aggregate within current record
	)
	perRec = map[string]*recAgg{}

	addExample := func(arr []string, val string, capN int) []string {
		if val == "" {
			return arr
		}
		for _, x := range arr {
			if x == val {
				return arr
			}
		}
		if len(arr) < capN {
			return append(arr, val)
		}
		return arr
	}

	mergeRecordIntoGlobal := func() {
		rep.TotalRecords++
		for path, ra := range perRec {
			ga := rep.Paths[path]
			ga.TotalCount += ra.count
			ga.RecordsWith++
			if ra.count > ga.MaxPerRecord {
				ga.MaxPerRecord = ra.count
			}
			// Merge example texts (cap to 3 unique)
			for _, ex := range ra.exTexts {
				if ex == "" {
					continue
				}
				dup := false
				for _, have := range ga.ExampleTexts {
					if have == ex {
						dup = true
						break
					}
				}
				if !dup && len(ga.ExampleTexts) < 3 {
					ga.ExampleTexts = append(ga.ExampleTexts, ex)
				}
			}
			// Merge attribute counts
			if ra.attrCounts != nil {
				if ga.AttrExamples == nil {
					ga.AttrExamples = map[string]map[string]int{}
				}
				for attr, vm := range ra.attrCounts {
					dst := ga.AttrExamples[attr]
					if dst == nil {
						dst = map[string]int{}
						ga.AttrExamples[attr] = dst
					}
					for val, c := range vm {
						dst[val] += c
					}
				}
			}
			rep.Paths[path] = ga
		}
		perRec = map[string]*recAgg{} // reset
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			// Treat truncation as clean EOF: return what we have (no merge if mid-record).
			if err == io.EOF || isTruncErr(err) {
				return rep, nil
			}
			return rep, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if !inRecord {
				if t.Name.Local == recordTag {
					inRecord = true
					relStack = relStack[:0]
					nodeStack = nodeStack[:0]
					perRec = map[string]*recAgg{}
				}
				continue
			}
			relStack = append(relStack, t.Name.Local)
			cp := make([]xml.Attr, len(t.Attr))
			copy(cp, t.Attr)
			nodeStack = append(nodeStack, frame{name: t.Name.Local, attrs: cp})

		case xml.CharData:
			if inRecord && len(nodeStack) > 0 {
				nodeStack[len(nodeStack)-1].text = append(nodeStack[len(nodeStack)-1].text, t...)
			}

		case xml.EndElement:
			if !inRecord {
				continue
			}
			// End of record → finalize
			if t.Name.Local == recordTag && len(relStack) == 0 {
				mergeRecordIntoGlobal()
				inRecord = false
				continue
			}
			// End of node within record → update per-record aggregates
			if len(relStack) > 0 && len(nodeStack) > 0 && relStack[len(relStack)-1] == t.Name.Local {
				path := strings.Join(relStack, "/")
				fr := nodeStack[len(nodeStack)-1]
				relStack = relStack[:len(relStack)-1]
				nodeStack = nodeStack[:len(nodeStack)-1]

				ra := perRec[path]
				if ra == nil {
					ra = &recAgg{attrCounts: map[string]map[string]int{}}
					perRec[path] = ra
				}
				ra.count++
				if txt := strings.TrimSpace(string(fr.text)); txt != "" {
					ra.exTexts = addExample(ra.exTexts, txt, 3)
				}
				for _, a := range fr.attrs {
					vm := ra.attrCounts[a.Name.Local]
					if vm == nil {
						vm = map[string]int{}
						ra.attrCounts[a.Name.Local] = vm
					}
					vm[a.Value]++
				}
			}
		}
	}
}

// StarterConfigFrom converts a DiscoverReport into an xmlparser.Config.
// Heuristic: paths with MaxPerRecord <= 1 become fields; others become lists.
// Only include paths with non-empty ExampleTexts (likely leaf text nodes).
func StarterConfigFrom(rep DiscoverReport) xmlparser.Config {
	fields := map[string]string{}
	lists := map[string]string{}
	for path, a := range rep.Paths {
		if len(a.ExampleTexts) == 0 {
			continue
		}
		if a.MaxPerRecord <= 1 {
			fields[path] = path
		} else {
			lists[path] = path
		}
	}
	return xmlparser.Config{
		RecordTag: rep.RecordTag,
		Fields:    fields,
		Lists:     lists,
	}
}

func GuessRecordTag(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false

	type el struct{ name string }
	var (
		stack    []el
		rootSeen bool
		rootName string
		counts   = map[string]int{}
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF || isTruncErr(err) {
				break
			}
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, el{name: t.Name.Local})
			if !rootSeen {
				rootSeen = true
				rootName = t.Name.Local
				continue
			}
			if len(stack) == 2 && stack[0].name == rootName {
				counts[t.Name.Local]++
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	best, bestN := "", 0
	for k, v := range counts {
		if v > bestN {
			best, bestN = k, v
		}
	}
	return best, nil
}

// isTruncErr returns true for typical encoding/xml truncation messages.
func isTruncErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "unexpected EOF") || strings.Contains(s, "XML syntax error")
}

// SortedPaths returns deterministic ordering for report paths.
func SortedPaths(rep DiscoverReport) []string {
	paths := make([]string, 0, len(rep.Paths))
	for p := range rep.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
