package config

import (
	"strings"
	"testing"

	"etl/internal/schema"
)

// hasIssue reports whether issues contains an Issue with the given severity,
// path, and a Message containing msgSubstr.
func hasIssue(t *testing.T, issues []Issue, sev IssueSeverity, path, msgSubstr string) bool {
	t.Helper()
	for _, iss := range issues {
		if iss.Severity == sev && iss.Path == path && strings.Contains(iss.Message, msgSubstr) {
			return true
		}
	}
	return false
}

/*
TestValidatePipeline_MissingJob verifies that a missing or empty Job field
produces a SeverityError with path "job".
*/
func TestValidatePipeline_MissingJob(t *testing.T) {
	p := Pipeline{
		Job: "", // missing/empty
		Source: Source{
			Kind: "file",
			File: SourceFile{Path: "input.csv"},
		},
		Parser: Parser{
			Kind: "csv",
			Options: Options{
				"expected_fields": float64(3), // avoid csv warning
			},
		},
		Storage: Storage{
			Kind: "postgres",
			DB: DBConfig{
				DSN:     "postgres://user@localhost/db",
				Table:   "public.t",
				Columns: []string{"id"},
			},
		},
		// No transforms/runtime needed here; we only care about job.
	}

	issues := ValidatePipeline(p)

	if !hasIssue(t, issues, SeverityError, "job", "job must not be empty") {
		t.Fatalf("expected SeverityError for job; got issues: %+v", issues)
	}
}

/*
TestValidatePipeline_ValidMinimal verifies that a well-formed pipeline produces
no issues (errors or warnings).
*/
func TestValidatePipeline_ValidMinimal(t *testing.T) {
	contract := schema.Contract{
		Fields: []schema.Field{
			{Name: "id", Type: "int", Required: true},
		},
	}

	p := Pipeline{
		Job: "test-job",
		Source: Source{
			Kind: "file",
			File: SourceFile{Path: "input.csv"},
		},
		Parser: Parser{
			Kind: "csv",
			Options: Options{
				"expected_fields": float64(1), // satisfy csv linter
			},
		},
		Transform: []Transform{
			{Kind: "normalize", Options: Options{}},
			{
				Kind: "validate",
				Options: Options{
					"contract": contract,
				},
			},
		},
		Storage: Storage{
			Kind: "postgres",
			DB: DBConfig{
				DSN:     "postgres://user@localhost/db",
				Table:   "public.t",
				Columns: []string{"id"},
			},
		},
		Runtime: RuntimeConfig{
			ReaderWorkers:    1,
			TransformWorkers: 1,
			LoaderWorkers:    1,
			BatchSize:        100,
			ChannelBuffer:    0,
		},
	}

	issues := ValidatePipeline(p)
	if len(issues) != 0 {
		t.Fatalf("expected no issues for valid pipeline; got: %+v", issues)
	}
}

/*
TestValidateSource_Cases exercises validateSource with missing kind, unknown
kind, and file-specific checks.
*/
func TestValidateSource_Cases(t *testing.T) {
	t.Run("missing_kind", func(t *testing.T) {
		s := Source{}
		issues := validateSource(s)
		if !hasIssue(t, issues, SeverityError, "source.kind", "must not be empty") {
			t.Fatalf("expected error for empty source.kind; got %+v", issues)
		}
	})

	t.Run("unknown_kind", func(t *testing.T) {
		s := Source{Kind: "weird"}
		issues := validateSource(s)
		if !hasIssue(t, issues, SeverityWarning, "source.kind", "unknown source kind") {
			t.Fatalf("expected warning for unknown source.kind; got %+v", issues)
		}
	})

	t.Run("file_missing_path", func(t *testing.T) {
		s := Source{Kind: "file", File: SourceFile{Path: "  "}}
		issues := validateSource(s)
		if !hasIssue(t, issues, SeverityError, "source.file.path", "non-empty path") {
			t.Fatalf("expected error for empty file.path; got %+v", issues)
		}
	})

	t.Run("file_ok", func(t *testing.T) {
		s := Source{Kind: "file", File: SourceFile{Path: "data.csv"}}
		issues := validateSource(s)
		if len(issues) != 0 {
			t.Fatalf("expected no issues; got %+v", issues)
		}
	})
}

/*
TestValidateParser_Cases exercises validateParser for empty kind, unknown kind,
and csv-specific option hints.
*/
func TestValidateParser_Cases(t *testing.T) {
	t.Run("missing_kind", func(t *testing.T) {
		p := Parser{}
		issues := validateParser(p)
		if !hasIssue(t, issues, SeverityError, "parser.kind", "must not be empty") {
			t.Fatalf("expected error for empty parser.kind; got %+v", issues)
		}
	})

	t.Run("unknown_kind", func(t *testing.T) {
		p := Parser{Kind: "weird"}
		issues := validateParser(p)
		if !hasIssue(t, issues, SeverityWarning, "parser.kind", "unknown parser kind") {
			t.Fatalf("expected warning for unknown parser.kind; got %+v", issues)
		}
	})

	t.Run("csv_missing_shape_hints", func(t *testing.T) {
		p := Parser{Kind: "csv", Options: Options{}}
		issues := validateParser(p)
		if !hasIssue(t, issues, SeverityWarning, "parser.options", "expected_fields") {
			t.Fatalf("expected warning for csv without shape hints; got %+v", issues)
		}
	})

	t.Run("csv_with_expected_fields_ok", func(t *testing.T) {
		p := Parser{Kind: "csv", Options: Options{"expected_fields": float64(5)}}
		issues := validateParser(p)
		if len(issues) != 0 {
			t.Fatalf("expected no issues; got %+v", issues)
		}
	})
}

/*
TestValidateTransforms_Cases covers:
  - empty transform list (warning),
  - empty kind (error),
  - unknown kind (warning),
  - validate without contract (warning),
  - validate with invalid contract (error),
  - validate with empty fields (warning),
  - validate with good contract (no issues).
*/
func TestValidateTransforms_Cases(t *testing.T) {
	t.Run("no_transforms", func(t *testing.T) {
		issues := validateTransforms(nil)
		if !hasIssue(t, issues, SeverityWarning, "transform", "no transforms configured") {
			t.Fatalf("expected warning for empty transform list; got %+v", issues)
		}
	})

	t.Run("empty_kind", func(t *testing.T) {
		ts := []Transform{{Kind: " "}}
		issues := validateTransforms(ts)
		if !hasIssue(t, issues, SeverityError, "transform[0].kind", "must not be empty") {
			t.Fatalf("expected error for empty transform kind; got %+v", issues)
		}
	})

	t.Run("unknown_kind", func(t *testing.T) {
		ts := []Transform{{Kind: "foobar", Options: Options{}}}
		issues := validateTransforms(ts)
		if !hasIssue(t, issues, SeverityWarning, "transform[0].kind", "unknown transform kind") {
			t.Fatalf("expected warning for unknown transform kind; got %+v", issues)
		}
	})

	t.Run("validate_no_contract", func(t *testing.T) {
		ts := []Transform{{Kind: "validate", Options: Options{}}}
		issues := validateTransforms(ts)
		if !hasIssue(t, issues, SeverityWarning, "transform[0].options.contract", "no contract") {
			t.Fatalf("expected warning for validate without contract; got %+v", issues)
		}
	})

	t.Run("validate_unmarshalable_contract", func(t *testing.T) {
		// Use a channel value which json.Marshal cannot handle.
		ts := []Transform{{
			Kind: "validate",
			Options: Options{
				"contract": make(chan int),
			},
		}}
		issues := validateTransforms(ts)
		if !hasIssue(t, issues, SeverityError, "transform[0].options.contract", "not JSON-marshable") {
			t.Fatalf("expected error for unmarshalable contract; got %+v", issues)
		}
	})

	t.Run("validate_invalid_contract_shape", func(t *testing.T) {
		ts := []Transform{{
			Kind: "validate",
			Options: Options{
				"contract": "not-an-object",
			},
		}}
		issues := validateTransforms(ts)
		if !hasIssue(t, issues, SeverityError, "transform[0].options.contract", "not a valid schema.Contract") {
			t.Fatalf("expected error for invalid contract shape; got %+v", issues)
		}
	})

	t.Run("validate_empty_contract_fields", func(t *testing.T) {
		contract := schema.Contract{} // no fields
		ts := []Transform{{
			Kind: "validate",
			Options: Options{
				"contract": contract,
			},
		}}
		issues := validateTransforms(ts)
		if !hasIssue(t, issues, SeverityWarning, "transform[0].options.contract", "no fields") {
			t.Fatalf("expected warning for empty contract fields; got %+v", issues)
		}
	})

	t.Run("validate_good_contract", func(t *testing.T) {
		contract := schema.Contract{
			Fields: []schema.Field{{Name: "id", Type: "int"}},
		}
		ts := []Transform{{
			Kind: "validate",
			Options: Options{
				"contract": contract,
			},
		}}
		issues := validateTransforms(ts)
		if len(issues) != 0 {
			t.Fatalf("expected no issues for valid validate transform; got %+v", issues)
		}
	})
}

/*
TestValidateStorage_Cases checks storage kind, DB DSN/table/columns, and
auto_create_table interactions.
*/
func TestValidateStorage_Cases(t *testing.T) {
	t.Run("missing_kind", func(t *testing.T) {
		s := Storage{}
		issues := validateStorage(s)
		if !hasIssue(t, issues, SeverityError, "storage.kind", "must not be empty") {
			t.Fatalf("expected error for empty storage.kind; got %+v", issues)
		}
	})

	t.Run("unknown_kind", func(t *testing.T) {
		s := Storage{Kind: "weird"}
		issues := validateStorage(s)
		if !hasIssue(t, issues, SeverityWarning, "storage.kind", "unknown storage kind") {
			t.Fatalf("expected warning for unknown storage.kind; got %+v", issues)
		}
	})

	t.Run("missing_dsn_table_columns", func(t *testing.T) {
		s := Storage{
			Kind: "postgres",
			DB:   DBConfig{},
		}
		issues := validateStorage(s)
		if !hasIssue(t, issues, SeverityError, "storage.db.dsn", "must not be empty") {
			t.Fatalf("expected error for empty dsn; got %+v", issues)
		}
		if !hasIssue(t, issues, SeverityError, "storage.db.table", "must not be empty") {
			t.Fatalf("expected error for empty table; got %+v", issues)
		}
		if !hasIssue(t, issues, SeverityError, "storage.db.columns", "must not be empty") {
			t.Fatalf("expected error for empty columns; got %+v", issues)
		}
	})

	t.Run("auto_create_without_columns", func(t *testing.T) {
		s := Storage{
			Kind: "postgres",
			DB: DBConfig{
				DSN:             "postgres://x",
				Table:           "public.t",
				Columns:         nil,
				AutoCreateTable: true,
			},
		}
		issues := validateStorage(s)
		if !hasIssue(t, issues, SeverityError, "storage.db.columns", "must not be empty") {
			t.Fatalf("expected error for empty columns; got %+v", issues)
		}
		if !hasIssue(t, issues, SeverityError, "storage.db.auto_create_table", "auto_create_table is true") {
			t.Fatalf("expected error for auto_create_table with no columns; got %+v", issues)
		}
	})

	t.Run("valid_storage", func(t *testing.T) {
		s := Storage{
			Kind: "postgres",
			DB: DBConfig{
				DSN:             "postgres://x",
				Table:           "public.t",
				Columns:         []string{"id", "name"},
				AutoCreateTable: false,
			},
		}
		issues := validateStorage(s)
		if len(issues) != 0 {
			t.Fatalf("expected no issues; got %+v", issues)
		}
	})
}

/*
TestValidateRuntime_Cases checks RuntimeConfig for negative worker counts,
non-positive batch sizes, and negative channel buffers.
*/
func TestValidateRuntime_Cases(t *testing.T) {
	t.Run("negatives", func(t *testing.T) {
		r := RuntimeConfig{
			ReaderWorkers:    -1,
			TransformWorkers: -2,
			LoaderWorkers:    -3,
			BatchSize:        -10,
			ChannelBuffer:    -4,
		}
		issues := validateRuntime(r)

		if !hasIssue(t, issues, SeverityError, "runtime.reader_workers", "must not be negative") {
			t.Fatalf("expected error for negative reader_workers; got %+v", issues)
		}
		if !hasIssue(t, issues, SeverityError, "runtime.transform_workers", "must not be negative") {
			t.Fatalf("expected error for negative transform_workers; got %+v", issues)
		}
		if !hasIssue(t, issues, SeverityError, "runtime.loader_workers", "must not be negative") {
			t.Fatalf("expected error for negative loader_workers; got %+v", issues)
		}
		if !hasIssue(t, issues, SeverityError, "runtime.channel_buffer", "must not be negative") {
			t.Fatalf("expected error for negative channel_buffer; got %+v", issues)
		}
		if !hasIssue(t, issues, SeverityWarning, "runtime.batch_size", "batch_size") {
			t.Fatalf("expected warning for non-positive batch_size; got %+v", issues)
		}
	})

	t.Run("zero_batch_size_warning_only", func(t *testing.T) {
		r := RuntimeConfig{
			ReaderWorkers:    1,
			TransformWorkers: 1,
			LoaderWorkers:    1,
			BatchSize:        0,
			ChannelBuffer:    0,
		}
		issues := validateRuntime(r)

		if !hasIssue(t, issues, SeverityWarning, "runtime.batch_size", "batch_size") {
			t.Fatalf("expected warning for batch_size=0; got %+v", issues)
		}

		// No errors expected.
		for _, iss := range issues {
			if iss.Severity == SeverityError {
				t.Fatalf("did not expect error for this runtime config; got %+v", issues)
			}
		}
	})

	t.Run("valid_runtime", func(t *testing.T) {
		r := RuntimeConfig{
			ReaderWorkers:    2,
			TransformWorkers: 2,
			LoaderWorkers:    2,
			BatchSize:        1000,
			ChannelBuffer:    128,
		}
		issues := validateRuntime(r)
		if len(issues) != 0 {
			t.Fatalf("expected no issues; got %+v", issues)
		}
	})
}
