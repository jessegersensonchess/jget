// Package config provides configuration models and helpers for ETL pipelines.
//
// This file adds a lightweight linter/validator for Pipeline values. It
// performs static checks over a decoded Pipeline and returns a list of issues
// (errors and warnings) that callers can surface in a CLI or tests.
package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"etl/internal/schema"
)

// IssueSeverity represents the severity of a configuration issue.
type IssueSeverity string

const (
	// SeverityError indicates a configuration error that should block execution.
	SeverityError IssueSeverity = "error"
	// SeverityWarning indicates a configuration warning that should be surfaced
	// to users but may not necessarily block execution.
	SeverityWarning IssueSeverity = "warning"
)

// Issue describes a single validation/lint finding for a Pipeline.
//
// Path is a dotted path into the config (e.g. "storage.kind",
// "transform[1].options.contract"). Message is human-readable.
type Issue struct {
	Severity IssueSeverity
	Path     string
	Message  string
}

// Error implements the error interface so an Issue can be treated as a single
// error in contexts that expect error.
func (i Issue) Error() string {
	return fmt.Sprintf("%s at %s: %s", i.Severity, i.Path, i.Message)
}

// ValidatePipeline performs static validation / linting of a Pipeline.
//
// It does not mutate the pipeline. Instead it returns a slice of Issue values.
// Callers may decide whether to treat warnings as fatal or not.
//
// Example:
//
//	var p config.Pipeline
//	if err := json.NewDecoder(r).Decode(&p); err != nil { ... }
//	issues := config.ValidatePipeline(p)
//	for _, iss := range issues {
//	    fmt.Printf("%s: %s: %s\n", iss.Severity, iss.Path, iss.Message)
//	}
func ValidatePipeline(p Pipeline) []Issue {
	var issues []Issue

	// Top-level pipeline checks.
	if strings.TrimSpace(p.Job) == "" {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "job",
			Message:  "job must not be empty; it is used for metrics labeling and identifying runs",
		})
	}
	issues = append(issues, validateSource(p.Source)...)
	issues = append(issues, validateParser(p.Parser)...)
	issues = append(issues, validateTransforms(p.Transform)...)
	issues = append(issues, validateStorage(p.Storage)...)
	issues = append(issues, validateRuntime(p.Runtime)...)

	return issues
}

// validateSource validates Source configuration.
func validateSource(s Source) []Issue {
	var issues []Issue

	// Kind is required.
	if strings.TrimSpace(s.Kind) == "" {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "source.kind",
			Message:  "source.kind must not be empty",
		})
		return issues
	}

	// Known source kinds. Unknown kinds are warnings (for forward compatibility).
	known := map[string]struct{}{
		"file": {},
	}
	if _, ok := known[s.Kind]; !ok {
		issues = append(issues, Issue{
			Severity: SeverityWarning,
			Path:     "source.kind",
			Message:  fmt.Sprintf("unknown source kind %q; ensure a matching implementation exists", s.Kind),
		})
	}

	// Kind-specific checks.
	switch s.Kind {
	case "file":
		if strings.TrimSpace(s.File.Path) == "" {
			issues = append(issues, Issue{
				Severity: SeverityError,
				Path:     "source.file.path",
				Message:  "file source requires a non-empty path",
			})
		}
	}

	return issues
}

// validateParser validates parser configuration.
func validateParser(p Parser) []Issue {
	var issues []Issue

	if strings.TrimSpace(p.Kind) == "" {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "parser.kind",
			Message:  "parser.kind must not be empty",
		})
		return issues
	}

	known := map[string]struct{}{
		"csv":  {},
		"xml":  {},
		"json": {},
	}
	if _, ok := known[p.Kind]; !ok {
		issues = append(issues, Issue{
			Severity: SeverityWarning,
			Path:     "parser.kind",
			Message:  fmt.Sprintf("unknown parser kind %q; ensure a matching implementation exists", p.Kind),
		})
	}

	// Parser-specific sanity checks (kept intentionally light).
	switch p.Kind {
	case "csv":
		// Example: warn if expected_fields is zero but header_map is also empty.
		expected := p.Options.Int("expected_fields", 0)
		headerMap := p.Options.Any("header_map")
		if expected == 0 && headerMap == nil {
			issues = append(issues, Issue{
				Severity: SeverityWarning,
				Path:     "parser.options",
				Message:  "csv parser has neither expected_fields nor header_map; consider constraining input shape",
			})
		}
	case "xml":
		// XML config is validated in parser/xml; nothing obvious to check here.

	case "json":
		// JSON config is validated in parser/json. not implemented
	}

	return issues
}

// validateTransforms validates the transform chain.
func validateTransforms(ts []Transform) []Issue {
	var issues []Issue

	if len(ts) == 0 {
		issues = append(issues, Issue{
			Severity: SeverityWarning,
			Path:     "transform",
			Message:  "no transforms configured; raw parsed records will be written as-is",
		})
		return issues
	}

	knownKinds := map[string]struct{}{
		"normalize": {},
		"coerce":    {},
		"dedup":     {},
		"require":   {},
		"validate":  {},
		"hash":      {},
	}

	for i, t := range ts {
		path := fmt.Sprintf("transform[%d].kind", i)
		if strings.TrimSpace(t.Kind) == "" {
			issues = append(issues, Issue{
				Severity: SeverityError,
				Path:     path,
				Message:  "transform kind must not be empty",
			})
			continue
		}
		if _, ok := knownKinds[t.Kind]; !ok {
			issues = append(issues, Issue{
				Severity: SeverityWarning,
				Path:     path,
				Message:  fmt.Sprintf("unknown transform kind %q; ensure a matching implementation exists", t.Kind),
			})
		}

		// Transform-specific checks.
		switch t.Kind {
		case "validate":
			raw := t.Options.Any("contract")
			if raw == nil {
				issues = append(issues, Issue{
					Severity: SeverityWarning,
					Path:     fmt.Sprintf("transform[%d].options.contract", i),
					Message:  "validate transform has no contract; it will not enforce schema-level rules",
				})
				break
			}
			// Try decoding into schema.Contract to catch structural issues early.
			b, err := json.Marshal(raw)
			if err != nil {
				issues = append(issues, Issue{
					Severity: SeverityError,
					Path:     fmt.Sprintf("transform[%d].options.contract", i),
					Message:  fmt.Sprintf("validate contract is not JSON-marshable: %v", err),
				})
				break
			}
			var c schema.Contract
			if err := json.Unmarshal(b, &c); err != nil {
				issues = append(issues, Issue{
					Severity: SeverityError,
					Path:     fmt.Sprintf("transform[%d].options.contract", i),
					Message:  fmt.Sprintf("validate contract is not a valid schema.Contract: %v", err),
				})
			} else if len(c.Fields) == 0 {
				issues = append(issues, Issue{
					Severity: SeverityWarning,
					Path:     fmt.Sprintf("transform[%d].options.contract", i),
					Message:  "validate contract has no fields; it will not enforce anything",
				})
			}
		}
	}

	return issues
}

// validateStorage validates storage configuration and DB settings.
func validateStorage(s Storage) []Issue {
	var issues []Issue

	if strings.TrimSpace(s.Kind) == "" {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "storage.kind",
			Message:  "storage.kind must not be empty",
		})
		return issues
	}

	known := map[string]struct{}{
		"postgres": {},
		"mysql":    {},
		"mssql":    {},
		"sqlite":   {},
	}
	if _, ok := known[s.Kind]; !ok {
		issues = append(issues, Issue{
			Severity: SeverityWarning,
			Path:     "storage.kind",
			Message:  fmt.Sprintf("unknown storage kind %q; ensure a matching backend is registered", s.Kind),
		})
	}

	// DB-specific checks (shared across backends).
	db := s.DB
	if strings.TrimSpace(db.DSN) == "" {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "storage.db.dsn",
			Message:  "storage.db.dsn must not be empty",
		})
	}
	if strings.TrimSpace(db.Table) == "" {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "storage.db.table",
			Message:  "storage.db.table must not be empty",
		})
	}
	if len(db.Columns) == 0 {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "storage.db.columns",
			Message:  "storage.db.columns must not be empty; at least one destination column is required",
		})
	}
	if db.AutoCreateTable && len(db.Columns) == 0 {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "storage.db.auto_create_table",
			Message:  "auto_create_table is true but no columns are defined; table inference cannot proceed",
		})
	}

	return issues
}

// validateRuntime validates RuntimeConfig for obvious misconfigurations
// (negative values, zero-sized batches, etc.).
func validateRuntime(r RuntimeConfig) []Issue {
	var issues []Issue

	if r.BatchSize <= 0 {
		issues = append(issues, Issue{
			Severity: SeverityWarning,
			Path:     "runtime.batch_size",
			Message:  fmt.Sprintf("batch_size=%d; non-positive batch sizes may hurt throughput", r.BatchSize),
		})
	}
	if r.ReaderWorkers < 0 {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "runtime.reader_workers",
			Message:  "reader_workers must not be negative",
		})
	}
	if r.TransformWorkers < 0 {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "runtime.transform_workers",
			Message:  "transform_workers must not be negative",
		})
	}
	if r.LoaderWorkers < 0 {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "runtime.loader_workers",
			Message:  "loader_workers must not be negative",
		})
	}
	if r.ChannelBuffer < 0 {
		issues = append(issues, Issue{
			Severity: SeverityError,
			Path:     "runtime.channel_buffer",
			Message:  "channel_buffer must not be negative",
		})
	}

	return issues
}
