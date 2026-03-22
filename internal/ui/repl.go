package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/peterh/liner"

	"github.com/QuakeWang/ori/internal/agent"
	"github.com/QuakeWang/ori/internal/llm"
	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/tool"
)

type repl struct {
	agent     *agent.Agent
	session   *session.Session
	renderer  *Renderer
	opts      agent.RunOptions
	workspace string
	sessionID string
	mode      string // "agent" or "shell"
	line      *liner.State
	histPath  string
}

func newREPL(ag *agent.Agent, sess *session.Session, r *Renderer,
	opts agent.RunOptions, workspace, sid string, histPath string,
) *repl {
	ln := liner.NewLiner()
	ln.SetCtrlCAborts(true)
	ln.SetTabCompletionStyle(liner.TabPrints)

	candidates := completionCandidates(ag)
	ln.SetCompleter(func(line string) []string {
		if !strings.HasPrefix(line, ",") {
			return nil
		}
		var matches []string
		for _, c := range candidates {
			if strings.HasPrefix(c, line) {
				matches = append(matches, c)
			}
		}
		return matches
	})

	if histPath != "" {
		if f, err := os.Open(histPath); err == nil {
			_, _ = ln.ReadHistory(f)
			_ = f.Close()
		}
	}

	return &repl{
		agent: ag, session: sess, renderer: r,
		opts: opts, workspace: workspace, sessionID: sid,
		mode: "agent", line: ln, histPath: histPath,
	}
}

// Run drives the interactive REPL loop.
func (r *repl) Run(ctx context.Context) error {
	defer r.close()

	for {
		input, err := r.line.Prompt(r.prompt())
		if err == liner.ErrPromptAborted {
			r.renderer.Output("Info", "Interrupted. Use ,quit to exit.")
			continue
		}
		if err != nil { // io.EOF or real error
			r.renderer.Goodbye()
			return nil
		}

		trimmed := strings.TrimSpace(input)
		if trimmed == "" {
			continue
		}
		r.line.AppendHistory(trimmed)

		switch trimmed {
		case "exit", "quit", ",exit", ",quit":
			r.renderer.Goodbye()
			return nil
		case ",mode":
			if r.mode == "agent" {
				r.mode = "shell"
			} else {
				r.mode = "agent"
			}
			r.renderer.Output("Info", fmt.Sprintf("Switched to %s mode", r.mode))
			continue
		case ",session":
			view := sessionView(r.agent, r.opts, r.workspace, r.sessionID)
			r.renderer.Output("Info", formatSessionDetails(view))
			continue
		}

		req := r.normalizeInput(trimmed)

		sp := startSpinner(r.renderer.out, "Thinking...")
		result, runErr := r.agent.RunTurn(ctx, r.session, llm.Input{Text: req}, r.opts)
		sp.Stop()

		if runErr != nil {
			if result != nil && result.Output != "" {
				r.renderer.Output("Error", result.Output)
			}
			r.renderer.Output("Error", runErr.Error())
			continue
		}

		kind := "Assistant"
		if strings.HasPrefix(trimmed, ",") {
			kind = "Command"
		}
		r.renderer.Output(kind, result.Output)
	}
}

func (r *repl) close() {
	if r.histPath != "" {
		if f, err := os.Create(r.histPath); err == nil {
			_, _ = r.line.WriteHistory(f)
			_ = f.Close()
		}
	}
	_ = r.line.Close()
}

func (r *repl) prompt() string {
	base := filepath.Base(filepath.Clean(r.workspace))
	if base == "" || base == "." {
		base = "ori"
	}
	sym := ">"
	if r.mode == "shell" {
		sym = ","
	}
	return fmt.Sprintf("%s %s ", base, sym)
}

func (r *repl) normalizeInput(input string) string {
	if r.mode != "shell" || strings.HasPrefix(input, ",") {
		return input
	}
	return "," + input
}

func formatSessionDetails(view SessionView) string {
	lines := []string{
		fmt.Sprintf("workspace: %s", view.Workspace),
		fmt.Sprintf("model: %s", view.Model),
		fmt.Sprintf("session: %s", view.SessionID),
		fmt.Sprintf("entries: %d", view.Entries),
		fmt.Sprintf("anchors: %d", view.Anchors),
		fmt.Sprintf("last token usage: %d", view.LastTokenUsage),
	}
	if view.LastAnchor != "" {
		lines = append(lines, fmt.Sprintf("last anchor: %s", view.LastAnchor))
	}
	return strings.Join(lines, "\n")
}

// --- completion candidates ---

func completionCandidates(ag *agent.Agent) []string {
	tools := ag.ListTools(tool.Filter{IncludeAll: true})
	set := map[string]bool{
		",exit": true, ",help": true, ",mode": true,
		",quit": true, ",session": true,
	}
	for _, t := range tools {
		set[","+t.Spec.Name] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// --- history path ---

func historyPath(home, workspace string) string {
	if strings.TrimSpace(home) == "" {
		return ""
	}
	dir := filepath.Join(home, "history")
	_ = os.MkdirAll(dir, 0o755)
	sum := sha256.Sum256([]byte(workspace))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".history")
}

// --- spinner ---

type spinner struct {
	w       io.Writer
	msg     string
	done    chan struct{}
	stopped sync.Once
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func startSpinner(w io.Writer, msg string) *spinner {
	s := &spinner{w: w, msg: msg, done: make(chan struct{})}
	go s.run()
	return s
}

func (s *spinner) run() {
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-s.done:
			return
		case <-t.C:
			_, _ = fmt.Fprintf(s.w, "\r%s %s ", spinnerFrames[i%len(spinnerFrames)], s.msg)
		}
	}
}

func (s *spinner) Stop() {
	s.stopped.Do(func() {
		close(s.done)
		_, _ = fmt.Fprintf(s.w, "\r%*s\r", len(s.msg)+4, "")
	})
}
