// etl/internal/ddl/create_test.go
package ddl

import (
	"strconv"
	"strings"
	"testing"
)

// TestBuildCreateTableSQL verifies that BuildCreateTableSQL generates the
// expected CREATE TABLE statements and surfaces appropriate errors for invalid
// inputs. It uses table-driven subtests to make individual scenarios easy to
// read and extend.
func TestBuildCreateTableSQL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		def         TableDef
		wantSQL     string
		wantErr     bool
		errContains string
	}{
		{
			name: "empty FQN returns error",
			def: TableDef{
				FQN:     "",
				Columns: []ColumnDef{{Name: "id", SQLType: "INT"}},
			},
			wantErr:     true,
			errContains: "table FQN must not be empty",
		},
		{
			name: "no columns returns error",
			def: TableDef{
				FQN:     "public.t",
				Columns: nil,
			},
			wantErr:     true,
			errContains: "at least one column is required",
		},
		{
			name: "column with empty name returns error",
			def: TableDef{
				FQN: "t",
				Columns: []ColumnDef{
					{Name: "", SQLType: "INT"},
				},
			},
			wantErr:     true,
			errContains: "column with empty name",
		},
		{
			name: "column with empty type returns error",
			def: TableDef{
				FQN: "t",
				Columns: []ColumnDef{
					{Name: "id", SQLType: ""},
				},
			},
			wantErr:     true,
			errContains: "missing SQLType",
		},
		{
			name: "single nullable column without default",
			def: TableDef{
				FQN: "t",
				Columns: []ColumnDef{
					{Name: "id", SQLType: "INT", Nullable: true},
				},
			},
			wantSQL: "CREATE TABLE t (\n  id INT\n);",
		},
		{
			name: "single non-nullable column without default",
			def: TableDef{
				FQN: "t",
				Columns: []ColumnDef{
					{Name: "id", SQLType: "INT", Nullable: false},
				},
			},
			wantSQL: "CREATE TABLE t (\n  id INT NOT NULL\n);",
		},
		{
			name: "column with default expression",
			def: TableDef{
				FQN: "t",
				Columns: []ColumnDef{
					{
						Name:     "created_at",
						SQLType:  "TIMESTAMP",
						Nullable: false,
						Default:  "CURRENT_TIMESTAMP",
					},
				},
			},
			wantSQL: "CREATE TABLE t (\n  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP\n);",
		},
		{
			name: "single primary key column",
			def: TableDef{
				FQN: "t",
				Columns: []ColumnDef{
					{
						Name:       "id",
						SQLType:    "INT",
						Nullable:   false,
						PrimaryKey: true,
					},
					{
						Name:     "name",
						SQLType:  "TEXT",
						Nullable: true,
					},
				},
			},
			wantSQL: "CREATE TABLE t (\n  id INT NOT NULL,\n  name TEXT,\n  PRIMARY KEY (id)\n);",
		},
		{
			name: "multiple primary key columns",
			def: TableDef{
				FQN: "t",
				Columns: []ColumnDef{
					{
						Name:       "id",
						SQLType:    "INT",
						Nullable:   false,
						PrimaryKey: true,
					},
					{
						Name:       "tenant_id",
						SQLType:    "INT",
						Nullable:   false,
						PrimaryKey: true,
					},
					{
						Name:     "payload",
						SQLType:  "TEXT",
						Nullable: true,
					},
				},
			},
			wantSQL: "CREATE TABLE t (\n  id INT NOT NULL,\n  tenant_id INT NOT NULL,\n  payload TEXT,\n  PRIMARY KEY (id, tenant_id)\n);",
		},
		{
			name: "whitespace around names and types is trimmed",
			def: TableDef{
				FQN: "  my_schema.my_table  ",
				Columns: []ColumnDef{
					{Name: "  col1  ", SQLType: "  INT  ", Nullable: true},
				},
			},
			// Note: FQN is trimmed, and column name/type are trimmed.
			wantSQL: "CREATE TABLE my_schema.my_table (\n  col1 INT\n);",
		},
		{
			name: "default with surrounding whitespace is trimmed",
			def: TableDef{
				FQN: "t",
				Columns: []ColumnDef{
					{
						Name:     "flag",
						SQLType:  "BOOLEAN",
						Nullable: false,
						Default:  "  false  ",
					},
				},
			},
			wantSQL: "CREATE TABLE t (\n  flag BOOLEAN NOT NULL DEFAULT false\n);",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotSQL, err := BuildCreateTableSQL(tt.def)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("BuildCreateTableSQL() error = nil, want non-nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("BuildCreateTableSQL() error = %q, want substring %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("BuildCreateTableSQL() unexpected error = %v", err)
			}
			if gotSQL != tt.wantSQL {
				t.Fatalf("BuildCreateTableSQL() =\n%s\nwant:\n%s", gotSQL, tt.wantSQL)
			}
		})
	}
}

// benchmarkSink is a package-level variable used to prevent the compiler from
// optimizing away the results of BuildCreateTableSQL in benchmarks.
var benchmarkSink string

// BenchmarkBuildCreateTableSQL_SmallSchema measures the performance of
// BuildCreateTableSQL for a small table definition with just a few columns.
//
// This is representative of many OLTP-style tables or small dimension tables.
func BenchmarkBuildCreateTableSQL_SmallSchema(b *testing.B) {
	def := TableDef{
		FQN: "small_table",
		Columns: []ColumnDef{
			{Name: "id", SQLType: "INT", Nullable: false, PrimaryKey: true},
			{Name: "name", SQLType: "TEXT", Nullable: true},
			{Name: "created_at", SQLType: "TIMESTAMP", Nullable: false, Default: "CURRENT_TIMESTAMP"},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sql, err := BuildCreateTableSQL(def)
		if err != nil {
			b.Fatalf("BuildCreateTableSQL() error = %v", err)
		}
		benchmarkSink = sql
	}
}

// BenchmarkBuildCreateTableSQL_LargeSchema measures the performance of
// BuildCreateTableSQL for a wider table definition with many columns.
//
// This simulates wide fact tables or denormalized analytical tables.
func BenchmarkBuildCreateTableSQL_LargeSchema(b *testing.B) {
	cols := make([]ColumnDef, 0, 64)
	for i := 0; i < 64; i++ {
		cols = append(cols, ColumnDef{
			Name:     "col_" + strconv.Itoa(i),
			SQLType:  "TEXT",
			Nullable: true,
		})
	}
	def := TableDef{
		FQN:     "large_table",
		Columns: cols,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sql, err := BuildCreateTableSQL(def)
		if err != nil {
			b.Fatalf("BuildCreateTableSQL() error = %v", err)
		}
		benchmarkSink = sql
	}
}
