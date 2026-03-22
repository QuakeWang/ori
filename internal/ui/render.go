package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// SessionView is the CLI-facing summary of a persisted session.
type SessionView struct {
	SessionID      string
	Workspace      string
	Model          string
	Entries        int
	Anchors        int
	LastAnchor     string
	LastTokenUsage int
}

// Renderer centralizes CLI formatting so all commands share the same style.
type Renderer struct {
	out    io.Writer
	errOut io.Writer
	mdR    *glamour.TermRenderer
}



var borderColors = map[string]lipgloss.Color{
	"Assistant": lipgloss.Color("4"),  // blue — matches Rich border_style="blue"
	"Command":   lipgloss.Color("2"),  // green — matches Rich border_style="green"
	"Error":     lipgloss.Color("1"),  // red — matches Rich border_style="red"
}

var infoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // bright_black — matches Rich style="bright_black"

// NewRenderer builds a renderer for the provided command streams.
func NewRenderer(out, errOut io.Writer) *Renderer {
	r := &Renderer{out: out, errOut: errOut}
	
	// Use the actual terminal width so that wide tables (e.g. slow-query
	// results with 9+ columns) render without column truncation.
	ww := termWidth(out) - 4
	
	if mdR, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle()),
		glamour.WithWordWrap(ww),
	); err == nil {
		r.mdR = mdR
	}
	return r
}

// Output renders a labeled block inside a bordered panel.
// Info output is rendered as plain styled text without panel, matching dod.
func (r *Renderer) Output(kind, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	w := r.out
	if kind == "Error" {
		w = r.errOut
	}

	// Info: plain styled text, no panel (matches dod's bright_black Text).
	if kind == "Info" {
		_, _ = fmt.Fprintln(w, infoStyle.Render(body))
		return
	}

	// Render markdown for assistant output.
	content := body
	if kind == "Assistant" && r.mdR != nil {
		if rendered, err := r.mdR.Render(body); err == nil {
			content = strings.TrimSpace(rendered)
			content = addTableRowSeparators(content)
		}
	}

	width := termWidth(w)
	color := borderColors[kind]
	if color == "" {
		color = lipgloss.Color("8")
	}

	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(color).
		Width(width - 2).
		Padding(0, 1)

	title := lipgloss.NewStyle().Bold(true).Foreground(color).Render(kind)
	_, _ = fmt.Fprintf(w, "%s\n", title)
	_, _ = fmt.Fprintln(w, panel.Render(content))
	_, _ = fmt.Fprintln(w)
}

// WelcomeChat prints the interactive chat banner inside a cyan panel.
func (r *Renderer) WelcomeChat(view SessionView) {
	r.welcomePanel("Ori", view, []string{
		"commands: prefix ',' for internal commands",
		"toggle: ,mode to switch agent/shell",
		"quit: ,quit | ,exit | Ctrl-D",
	})
}

// WelcomeRun prints the one-shot run banner inside a cyan panel.
func (r *Renderer) WelcomeRun(view SessionView) {
	r.welcomePanel("Ori", view, nil)
}

// SessionStatus prints a compact session summary.
func (r *Renderer) SessionStatus(view SessionView) {
	parts := []string{
		fmt.Sprintf("session=%s", view.SessionID),
		fmt.Sprintf("entries=%d", view.Entries),
		fmt.Sprintf("anchors=%d", view.Anchors),
		fmt.Sprintf("tokens=%d", view.LastTokenUsage),
	}
	if view.LastAnchor != "" {
		parts = append(parts, fmt.Sprintf("anchor=%s", view.LastAnchor))
	}
	_, _ = fmt.Fprintln(r.out, infoStyle.Render("[session] "+strings.Join(parts, " | ")))
	_, _ = fmt.Fprintln(r.out)
}

// Table prints headers and rows using tabwriter.
func (r *Renderer) Table(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(r.out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))
	div := make([]string, len(headers))
	for i, h := range headers {
		div[i] = strings.Repeat("-", len(h))
	}
	_, _ = fmt.Fprintln(w, strings.Join(div, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	_ = w.Flush()
}

// Goodbye prints the exit message.
func (r *Renderer) Goodbye() { _, _ = fmt.Fprintln(r.out, "Bye.") }

func (r *Renderer) welcomePanel(title string, view SessionView, extra []string) {
	lines := []string{
		fmt.Sprintf("model: %s", view.Model),
		fmt.Sprintf("session: %s", view.SessionID),
	}
	lines = append(lines, extra...)
	body := strings.Join(lines, "\n")

	width := termWidth(r.out)
	cyan := lipgloss.Color("6") // matches Rich border_style="cyan"
	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cyan).
		Width(width - 2).
		Padding(0, 1)

	heading := lipgloss.NewStyle().Bold(true).Foreground(cyan).Render(title)
	_, _ = fmt.Fprintf(r.out, "%s\n", heading)
	_, _ = fmt.Fprintln(r.out, panel.Render(body))
	_, _ = fmt.Fprintln(r.out)
}


func termWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return 80
}

func glamourStyle() string {
	if s := os.Getenv("ORI_GLAMOUR_STYLE"); s != "" {
		return s
	}
	return "dark"
}

// addTableRowSeparators inserts horizontal separator lines between data rows
// in tables rendered by glamour. Glamour uses lipgloss/table which renders:
//
//	Header Row          (contains │)
//	──────┼──────       (header separator, contains ─ and ┼)
//	Data Row 1          (contains │)
//	Data Row 2          (contains │)
//
// This function detects the header separator, then inserts a copy of it
// between every pair of data rows that follow.
func addTableRowSeparators(s string) string {
	lines := strings.Split(s, "\n")
	result := make([]string, 0, len(lines)*2)

	for i := 0; i < len(lines); i++ {
		result = append(result, lines[i])

		// Detect a header separator line: contains ─ and ┼.
		if !isTableSeparator(lines[i]) {
			continue
		}
		sep := lines[i]

		// Insert this separator between every subsequent pair of data rows.
		for i+1 < len(lines) && isTableDataRow(lines[i+1]) {
			i++
			result = append(result, lines[i])
			// If the next line is also a data row, insert separator after current.
			if i+1 < len(lines) && isTableDataRow(lines[i+1]) {
				result = append(result, sep)
			}
		}
	}
	return strings.Join(result, "\n")
}

// isTableSeparator checks if a line is a glamour table header separator
// (contains box-drawing horizontal line and cross characters).
func isTableSeparator(line string) bool {
	return strings.Contains(line, "─") && strings.Contains(line, "┼")
}

// isTableDataRow checks if a line is a glamour table data row
// (contains the vertical bar box-drawing character).
func isTableDataRow(line string) bool {
	return strings.Contains(line, "│")
}
