// Package config defines the canonical, JSON-serializable configuration model
// for the ETL application. It is intentionally small, explicit, and dependency-
// free so that pipelines can be loaded from disk (or other sources) and passed
// through the program without additional glue code.
//
// Design goals:
//
//  1. Stability: Changes to this package should be additive and backwards-
//     compatible whenever possible.
//  2. Clarity: Field names in Go mirror the JSON structure used in pipeline
//     files under configs/pipelines/*.json.
//  3. Minimalism: No third-party config libraries; decoding is performed by the
//     standard library, with a light Options helper for typed access.
//
// Example (trimmed):
//
//	{
//	  "source":   { "kind": "file", "file": { "path": "path/to.csv" } },
//	  "parser":   { "kind": "csv", "options": { "has_header": true } },
//	  "transform":[
//	    { "kind": "validate", "options": { "contract": { "name": "x", "fields": [...] } } }
//	  ],
//	  "storage":  { "kind": "postgres", "postgres": { "dsn": "...", "table": "public.t" } }
//	}
package config

import "encoding/json"

// Pipeline describes the full ETL pipeline in JSON. It is the top-level object
// decoded from a pipeline file (e.g., configs/pipelines/*.json).
type Pipeline struct {
	// Job is a value used for metrics labeling
	Job string `json:"job"`

	// Source describes where input data comes from (e.g., local file).
	Source Source `json:"source"`

	// Parser configures how raw bytes are turned into records (e.g., CSV).
	Parser Parser `json:"parser"`

	// Transform lists the ordered transformations applied to parsed records.
	// Each transform has a kind and an options bag. The options shape is defined
	// by the transform implementation.
	Transform []Transform `json:"transform"`

	// Storage describes where transformed records are written (e.g., Postgres).
	Storage Storage       `json:"storage"`
	Runtime RuntimeConfig `json:"runtime"`
}

// RuntimeConfig controls concurrency, batching, and channel buffer sizes.
type RuntimeConfig struct {
	ReaderWorkers    int `json:"reader_workers"`
	TransformWorkers int `json:"transform_workers"`
	LoaderWorkers    int `json:"loader_workers"`
	BatchSize        int `json:"batch_size"`
	ChannelBuffer    int `json:"channel_buffer"`
}

// Source identifies the data source. Additional kinds can be added over time.
type Source struct {
	// Kind selects the source implementation. Current value: "file".
	Kind string `json:"kind"`

	// File carries options for the "file" source kind.
	File SourceFile `json:"file"`
}

// SourceFile holds configuration for the "file" source kind.
type SourceFile struct {
	// Path is the local filesystem path to the input file.
	Path string `json:"path"`
}

// Parser selects how to parse the raw source into logical rows/columns.
type Parser struct {
	// Kind selects the parser implementation. Current value: "csv".
	Kind string `json:"kind"`

	// Options is a free-form map interpreted by the parser implementation.
	// For CSV, typical keys include:
	//   has_header (bool), comma (string), trim_space (bool),
	//   expected_fields (int), header_map (object)
	Options Options `json:"options"`
}

// Transform defines a single transformation step. The sequence of steps forms
// the transformation chain executed by the pipeline.
type Transform struct {
	// Kind selects the transform implementation (e.g., "normalize", "validate",
	// "coerce", "dedupe", "require"). Implementations define their own options.
	Kind string `json:"kind"`

	// Options is a free-form map interpreted by the selected transform.
	Options Options `json:"options"`
}

// Storage selects the sink used to persist transformed records.
type Storage struct {
	// Kind selects the storage implementation. Current value: "postgres".
	Kind string `json:"kind"`

	// Postgres carries options for the "postgres" storage kind.
	//Postgres StoragePostgres `json:"postgres"`
	DB DBConfig `yaml:"db" json:"db"`
}

// StoragePostgres is now DBConfig, configures the DB sink.
type DBConfig struct {
	// DSN is the connection string for pgx/pgxpool (e.g., postgresql://...).
	DSN string `json:"dsn"`

	// Table is the fully qualified table name (e.g., "public.my_table").
	Table string `json:"table"`

	// Columns enumerates the destination columns in the order used for COPY and
	// INSERT/SELECT (and historically upsert). Do NOT include auto-generated
	// identity/serial columns if the database should populate them.
	Columns []string `json:"columns"`

	// KeyColumns identifies the logical key used by certain storage operations
	// (e.g., delete-and-insert, dedup semantics). This is NOT required to be a
	// database primary key but should uniquely identify a record at the desired
	// grain. Leave empty if not applicable.
	KeyColumns []string `json:"key_columns"`

	// DateColumn optionally names a column that represents the record's effective
	// date (or similar). Some storage strategies may use this to prune/merge.
	// Leave empty if not used.
	DateColumn string `json:"date_column"`

	// AutoCreateTable should the process automatically create the DB table
	AutoCreateTable bool `json:"auto_create_table"`
}

// Options is a small helper to fetch typed values from arbitrary JSON maps
// without introducing third-party configuration libraries. It purposefully
// performs only minimal type coercion and returns provided defaults when a key
// is absent or of an unexpected type.
//
// Options is used for parser/transform-specific configuration where the shape
// varies by implementation.
type Options map[string]any

// String returns the string value for key or def if key is missing or not a string.
func (o Options) String(key, def string) string {
	if v, ok := o[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// Bool returns the bool value for key or def if key is missing or not a bool.
func (o Options) Bool(key string, def bool) bool {
	if v, ok := o[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// Int returns the int value for key or def. JSON numbers are decoded as
// float64 by encoding/json, so this method accepts float64 and casts to int.
// If the value is neither float64 nor int, def is returned.
func (o Options) Int(key string, def int) int {
	if v, ok := o[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return def
}

// Rune returns the first rune of a string value for key, or def if key is
// missing or empty. This is useful for single-character parser settings such as
// a CSV delimiter.
func (o Options) Rune(key string, def rune) rune {
	if v, ok := o[key]; ok {
		if s, ok := v.(string); ok && len(s) > 0 {
			return []rune(s)[0]
		}
	}
	return def
}

// StringMap returns a map[string]string for key when the value is an object
// whose values are strings. Non-string values are ignored. Returns an empty map
// when the key is missing or the value is not an object.
func (o Options) StringMap(key string) map[string]string {
	res := map[string]string{}
	if v, ok := o[key]; ok {
		if m, ok := v.(map[string]any); ok {
			for k, vv := range m {
				if s, ok := vv.(string); ok {
					res[k] = s
				}
			}
		}
	}
	return res
}

// StringSlice returns a []string for key when the value is an array of strings
// (or an array of interface values containing strings). Returns nil when the
// key is missing or the value is not an array.
func (o Options) StringSlice(key string) []string {
	if v, ok := o[key]; ok {
		switch vv := v.(type) {
		case []any:
			out := make([]string, 0, len(vv))
			for _, x := range vv {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
			return out
		case []string:
			return vv
		}
	}
	return nil
}

// Any returns the raw value for key (which may itself be a nested
// map[string]any, []any, or primitive). This is useful for retrieving nested
// configuration blocks that will be unmarshaled into a typed struct by the
// caller (e.g., an inline validation contract).
func (o Options) Any(key string) any {
	if v, ok := o[key]; ok {
		return v
	}
	return nil
}

// UnmarshalJSON implements json.Unmarshaler so that a missing or null "options"
// object in JSON decodes to a non-nil, empty Options map. This simplifies call
// sites by removing the need to nil-check Options values.
func (o *Options) UnmarshalJSON(b []byte) error {
	var tmp map[string]any
	if len(b) == 0 || string(b) == "null" {
		*o = Options{}
		return nil
	}
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	*o = Options(tmp)
	return nil
}
