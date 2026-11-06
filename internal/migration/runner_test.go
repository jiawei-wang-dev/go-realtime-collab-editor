package migration

import (
	"database/sql"
	"strings"
	"testing"
)

func TestExpectedAddedColumn(t *testing.T) {
	tests := []struct {
		statement string
		table     string
		column    string
		expected  columnSchema
	}{
		{
			statement: "ALTER TABLE documents ADD COLUMN fencing_token BIGINT UNSIGNED NOT NULL DEFAULT 0",
			table:     "documents",
			column:    "fencing_token",
			expected: columnSchema{
				columnType:    "bigint unsigned",
				dataType:      "bigint",
				isNullable:    "NO",
				columnDefault: sql.NullString{String: "0", Valid: true},
			},
		},
		{
			statement: "ALTER TABLE `documents` ADD COLUMN `owner_node_id` VARCHAR(128) NULL",
			table:     "documents",
			column:    "owner_node_id",
			expected: columnSchema{
				columnType: "varchar(128)",
				dataType:   "varchar",
				isNullable: "YES",
			},
		},
	}
	for _, test := range tests {
		table, column, expected, err := expectedAddedColumn(test.statement)
		if err != nil {
			t.Fatal(err)
		}
		if table != test.table || column != test.column || expected != test.expected {
			t.Fatalf("parsed = %s.%s %#v, want %s.%s %#v", table, column, expected, test.table, test.column, test.expected)
		}
	}
}

func TestCompareColumnSchemaReportsEveryField(t *testing.T) {
	expected := columnSchema{
		columnType:    "bigint unsigned",
		dataType:      "bigint",
		isNullable:    "NO",
		columnDefault: sql.NullString{String: "0", Valid: true},
	}
	actual := columnSchema{
		columnType:    "int unsigned",
		dataType:      "int",
		isNullable:    "YES",
		columnDefault: sql.NullString{String: "1", Valid: true},
		extra:         "auto_increment",
	}
	err := compareColumnSchema("documents", "fencing_token", expected, actual)
	if err == nil {
		t.Fatal("incompatible schema was accepted")
	}
	for _, field := range []string{"COLUMN_TYPE", "DATA_TYPE", "IS_NULLABLE", "COLUMN_DEFAULT", "EXTRA"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("error %q does not report %s", err, field)
		}
	}
}

func TestExpectedAddedColumnRejectsUnknownDefinition(t *testing.T) {
	if _, _, _, err := expectedAddedColumn("ALTER TABLE documents ADD COLUMN fencing_token JSON NULL"); err == nil {
		t.Fatal("unsupported column definition was accepted")
	}
}
