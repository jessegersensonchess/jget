// internal/ddl/create.go

// Package ddl defines a small, backend-agnostic model for SQL DDL and helpers
// to render simple CREATE TABLE statements from that model.
//
// The goal of this package is to stay generic: it does not assume any specific
// SQL dialect. In particular, it:
//
//   - Does not quote identifiers; it emits TableDef.FQN and ColumnDef.Name as-is.
//   - Does not insert dialect-specific clauses such as IF NOT EXISTS.
//   - Treats ColumnDef.Default as raw SQL (the caller is responsible for
//     safety and dialect correctness).
//
// Backend-specific packages (e.g., internal/storage/postgres/ddl) are expected
// to adapt this model to their dialect as needed: they may wrap or reimplement
// BuildCreateTableSQL using the same TableDef/ColumnDef types.
package ddl

import (
	"fmt"
	"strings"
)

// BuildCreateTableSQL renders a generic CREATE TABLE statement from a TableDef.
//
// Rules:
//
//   - t.FQN must be non-empty; it is emitted verbatim as the table name.
//
//   - Each column must have a non-empty Name and SQLType.
//
//   - A column is rendered as:
//
//     <Name> <SQLType> [NOT NULL] [DEFAULT <Default>]
//
//     where NOT NULL is added when Nullable == false.
//
//   - Columns with PrimaryKey == true are collected and rendered as a separate
//     PRIMARY KEY (<col1>, <col2>, ...) clause at the end of the column list.
//
//   - The resulting statement has the form:
//
//     CREATE TABLE <FQN> (
//     <col1-def>,
//     <col2-def>,
//     ...,
//     [PRIMARY KEY (<pk-cols>)]
//     );
//
// This function does not attempt to be fully portable or exhaustive; it is
// intended as a simple, deterministic baseline that backends can wrap or
// replace with dialect-specific builders.
func BuildCreateTableSQL(t TableDef) (string, error) {
	fqn := strings.TrimSpace(t.FQN)
	if fqn == "" {
		return "", fmt.Errorf("ddl: table FQN must not be empty")
	}
	if len(t.Columns) == 0 {
		return "", fmt.Errorf("ddl: at least one column is required")
	}

	cols := make([]string, 0, len(t.Columns)+1)
	pks := make([]string, 0, len(t.Columns))

	for _, c := range t.Columns {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return "", fmt.Errorf("ddl: column with empty name in table %s", fqn)
		}
		typ := strings.TrimSpace(c.SQLType)
		if typ == "" {
			return "", fmt.Errorf("ddl: column %s missing SQLType", name)
		}

		var sb strings.Builder
		sb.WriteString(name)
		sb.WriteByte(' ')
		sb.WriteString(typ)

		if !c.Nullable {
			sb.WriteString(" NOT NULL")
		}

		if def := strings.TrimSpace(c.Default); def != "" {
			sb.WriteString(" DEFAULT ")
			// Default is emitted as raw SQL expression.
			sb.WriteString(def)
		}

		cols = append(cols, sb.String())

		if c.PrimaryKey {
			pks = append(pks, name)
		}
	}

	if len(pks) > 0 {
		cols = append(cols, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(pks, ", ")))
	}

	stmt := fmt.Sprintf(
		"CREATE TABLE %s (\n  %s\n);",
		fqn,
		strings.Join(cols, ",\n  "),
	)

	return stmt, nil
}
