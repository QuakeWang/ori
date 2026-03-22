package doris

import (
	"fmt"
	"strings"
)

const maxTableCellWidth = 200

// FormatResultSet renders Doris rows either as a Markdown table or labeled text blocks.
func FormatResultSet(result *ResultSet, maxRows int, columns []string) string {
	if result == nil || len(result.Rows) == 0 {
		return "(no rows returned)"
	}
	if maxRows <= 0 {
		maxRows = len(result.Rows)
	}

	selectedColumns := selectColumns(result.Columns, columns)
	displayRows := result.Rows
	truncated := false
	if len(displayRows) > maxRows {
		displayRows = displayRows[:maxRows]
		truncated = true
	}

	var out string
	if shouldRenderAsText(displayRows, selectedColumns) {
		out = formatRowsAsText(result.Rows, displayRows, selectedColumns, truncated)
	} else {
		out = formatRowsAsTable(result.Rows, displayRows, selectedColumns, truncated)
	}
	return out
}

func selectColumns(all []string, requested []string) []string {
	if len(requested) == 0 {
		return append([]string(nil), all...)
	}

	selected := make([]string, 0, len(requested))
	exists := make(map[string]bool, len(all))
	for _, column := range all {
		exists[column] = true
	}
	for _, column := range requested {
		column = strings.TrimSpace(column)
		if column == "" {
			continue
		}
		if exists[column] {
			selected = append(selected, column)
		}
	}
	if len(selected) == 0 {
		return append([]string(nil), all...)
	}
	return selected
}

func shouldRenderAsText(rows []map[string]string, columns []string) bool {
	if len(rows) == 0 || len(rows) > 3 {
		return false
	}

	maxValueLength := 0
	for _, row := range rows {
		for _, column := range columns {
			if length := len(row[column]); length > maxValueLength {
				maxValueLength = length
			}
		}
	}
	return maxValueLength > 200
}

func formatRowsAsTable(allRows, rows []map[string]string, columns []string, truncated bool) string {
	lines := make([]string, 0, len(rows)+4)
	lines = append(lines, "| "+strings.Join(columns, " | ")+" |")

	separator := make([]string, len(columns))
	for i := range separator {
		separator[i] = "---"
	}
	lines = append(lines, "| "+strings.Join(separator, " | ")+" |")

	for _, row := range rows {
		cells := make([]string, 0, len(columns))
		for _, column := range columns {
			value := strings.ReplaceAll(row[column], "\n", " ")
			value = strings.ReplaceAll(value, "|", `\|`)
			if len(value) > maxTableCellWidth {
				value = value[:maxTableCellWidth-3] + "..."
			}
			cells = append(cells, value)
		}
		lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
	}

	result := strings.Join(lines, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n(%d more rows truncated)", len(allRows)-len(rows))
	}
	result += fmt.Sprintf("\n\n(%s)", rowCountText(len(allRows)))
	return result
}

func formatRowsAsText(allRows, rows []map[string]string, columns []string, truncated bool) string {
	parts := make([]string, 0, len(rows)*len(columns)+2)
	for i, row := range rows {
		if len(rows) > 1 {
			parts = append(parts, fmt.Sprintf("--- Row %d ---", i+1))
		}
		for _, column := range columns {
			value := row[column]
			if value == "" {
				continue
			}
			if strings.Contains(value, "\n") || len(value) > maxTableCellWidth {
				parts = append(parts, fmt.Sprintf("%s:\n```\n%s\n```", column, value))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s: %s", column, value))
		}
	}

	if truncated {
		parts = append(parts, fmt.Sprintf("(%d more rows truncated)", len(allRows)-len(rows)))
	}
	parts = append(parts, fmt.Sprintf("(%s)", rowCountText(len(allRows))))
	return strings.Join(parts, "\n\n")
}

func rowCountText(count int) string {
	if count == 1 {
		return "1 row total"
	}
	return fmt.Sprintf("%d rows total", count)
}
