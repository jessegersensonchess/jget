package extracthtml

// Mapping represents one extraction rule.
type Mapping struct {
	Selector string `json:"selector"`        // evaluated relative to doc OR record (depending on mode)
	Extract  string `json:"extract"`         // "text", "attr", "js_email"
	Attr     string `json:"attr,omitempty"`  // used when Extract == "attr"
	JSONPath string `json:"json_path"`       // key name in output object
	Match    string `json:"match,omitempty"` // optional regex filter (applies to extracted value)
	All      bool   `json:"all,omitempty"`   // optional: collect all matches into []string
}

// MappingFile describes the mappings.json file.
type MappingFile struct {
	RecordSelector string    `json:"record_selector,omitempty"` // if set => record mode
	Mappings       []Mapping `json:"mappings"`
}
