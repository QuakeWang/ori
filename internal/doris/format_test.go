package doris

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatResultSet_Table(t *testing.T) {
	result := &ResultSet{
		Columns: []string{"db", "tbl", "sql_preview"},
		Rows: []map[string]string{
			{"db": "sales", "tbl": "orders", "sql_preview": "select 1 | from dual"},
			{"db": "sales", "tbl": "items", "sql_preview": "line1\nline2"},
		},
	}

	text := FormatResultSet(result, 10, []string{"tbl", "sql_preview"})

	assert.Contains(t, text, "| tbl | sql_preview |")
	assert.Contains(t, text, `select 1 \| from dual`)
	assert.Contains(t, text, "line1 line2")
	assert.Contains(t, text, "(2 rows total)")
}

func TestFormatResultSet_TextBlocksForDDL(t *testing.T) {
	ddl := "CREATE TABLE t (\n  k1 INT,\n  v1 VARCHAR(20)\n)\nENGINE=OLAP\nDUPLICATE KEY(k1)"
	result := &ResultSet{
		Columns: []string{"Table", "Create Table"},
		Rows: []map[string]string{
			{"Table": "t", "Create Table": ddl + strings.Repeat(" -- extra", 30)},
		},
	}

	text := FormatResultSet(result, 10, nil)

	assert.Contains(t, text, "Table: t")
	assert.Contains(t, text, "Create Table:")
	assert.Contains(t, text, "```")
	assert.Contains(t, text, "(1 row total)")
}

func TestFormatResultSet_Truncation(t *testing.T) {
	result := &ResultSet{
		Columns: []string{"id"},
		Rows: []map[string]string{
			{"id": "1"},
			{"id": "2"},
			{"id": "3"},
		},
	}

	text := FormatResultSet(result, 2, nil)

	assert.Contains(t, text, "(1 more rows truncated)")
	assert.Contains(t, text, "(3 rows total)")
}
