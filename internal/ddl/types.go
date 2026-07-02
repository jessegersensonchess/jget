package ddl

// ColumnDef describes a single column in a table definition produced or
// consumed by ddl. It intentionally uses simple, database-agnostic fields.
//
// Fields:
//   - Name: logical column name (unquoted; quoting/escaping happens at render time)
//   - SQLType: target SQL type (e.g., TEXT, BIGINT, TIMESTAMPTZ)
//   - Nullable: whether NULL is allowed
//   - PrimaryKey: whether the column is part of the primary key (not used by all generators)
//   - Default: raw default expression (e.g., 'anon', CURRENT_TIMESTAMP)
type ColumnDef struct {
	Name       string
	SQLType    string
	Nullable   bool
	PrimaryKey bool
	Default    string
}

// TableDef holds the fully-qualified table name (FQN) and an ordered list of
// columns. The FQN is expected in dotted form (e.g., "schema.table") and will
// be quoted/escaped by renderers as needed.
type TableDef struct {
	FQN     string
	Columns []ColumnDef
}
