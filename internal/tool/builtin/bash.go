package builtin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuakeWang/ori/internal/tool"
)

const defaultCommandTimeoutSeconds = 30

// syncBuffer is a concurrency-safe byte buffer that implements io.Writer.
// It is used as cmd.Stdout/cmd.Stderr so that output can be read safely
// while the process is still running.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// shellEntry tracks a running or completed shell process.
type shellEntry struct {
	id         string
	sessionID  string
	cmd        *exec.Cmd
	output     *syncBuffer
	done       chan struct{}
	returncode *int
	mu         sync.Mutex // protects returncode only
}

// shellManager manages background shell processes.
type shellManager struct {
	mu     sync.Mutex
	shells map[string]*shellEntry
}

var manager = &shellManager{
	shells: make(map[string]*shellEntry),
}

var completedShellRetentionNanos atomic.Int64

func init() {
	setCompletedShellRetention(5 * time.Minute)
}

func completedShellRetention() time.Duration {
	return time.Duration(completedShellRetentionNanos.Load())
}

func setCompletedShellRetention(retention time.Duration) {
	completedShellRetentionNanos.Store(int64(retention))
}

func (sm *shellManager) start(sessionID, command, cwd string) (*shellEntry, error) {
	id, err := newShellID()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("sh", "-c", command)
	if cwd != "" {
		cmd.Dir = cwd
	}

	entry := &shellEntry{
		id:        id,
		sessionID: sessionID,
		cmd:       cmd,
		output:    &syncBuffer{},
		done:      make(chan struct{}),
	}

	// Capture combined output via concurrency-safe buffer.
	cmd.Stdout = entry.output
	cmd.Stderr = entry.output

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Wait in background goroutine.
	go func() {
		_ = cmd.Wait()
		entry.mu.Lock()
		code := cmd.ProcessState.ExitCode()
		entry.returncode = &code
		entry.mu.Unlock()
		close(entry.done)

		retention := completedShellRetention()
		if retention > 0 {
			time.AfterFunc(retention, func() {
				sm.removeIfSame(entry.id, entry)
			})
		}
	}()

	sm.mu.Lock()
	sm.shells[id] = entry
	sm.mu.Unlock()

	return entry, nil
}

func (sm *shellManager) get(id string) (*shellEntry, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	e, ok := sm.shells[id]
	return e, ok
}

func (sm *shellManager) getForSession(sessionID, id string) (*shellEntry, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	e, ok := sm.shells[id]
	if !ok || e.sessionID != sessionID {
		return nil, false
	}
	return e, true
}

func (sm *shellManager) requireForSession(sessionID, id string) (*shellEntry, error) {
	entry, ok := sm.getForSession(sessionID, id)
	if !ok {
		return nil, fmt.Errorf("shell %q not found", id)
	}
	return entry, nil
}

func (sm *shellManager) remove(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.shells, id)
}

func (sm *shellManager) removeIfSame(id string, target *shellEntry) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if current, ok := sm.shells[id]; ok && current == target {
		delete(sm.shells, id)
	}
}

func (sm *shellManager) count() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return len(sm.shells)
}

func (sm *shellManager) terminate(sessionID, id string) error {
	e, ok := sm.getForSession(sessionID, id)
	if !ok {
		return fmt.Errorf("shell %q not found", id)
	}
	if e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
	<-e.done
	sm.removeIfSame(id, e)
	return nil
}

func newShellID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate shell id: %w", err)
	}
	return "bsh-" + hex.EncodeToString(buf), nil
}

func (e *shellEntry) returnCodeValue() (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.returncode == nil {
		return 0, false
	}
	return *e.returncode, true
}

func foregroundOutputText(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return "(no output)"
	}
	return output
}

func pagedOutput(output string, offset int, limit *int) (chunk string, nextOffset, total int) {
	runes := []rune(output)
	total = len(runes)
	start := max(0, min(offset, len(runes)))
	end := len(runes)
	if limit != nil {
		end = min(len(runes), start+max(0, *limit))
	}

	chunk = strings.TrimRight(string(runes[start:end]), " \n\r\t")
	if chunk == "" {
		chunk = "(no output)"
	}
	return chunk, end, total
}

func shellStatusText(entry *shellEntry) (status, exitCode string) {
	if code, ok := entry.returnCodeValue(); ok {
		return "exited", fmt.Sprintf("%d", code)
	}
	return "running", "null"
}

type bashInput struct {
	Cmd            string `json:"cmd"`
	Cwd            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Background     bool   `json:"background,omitempty"`
}

type bashOutputInput struct {
	ShellID string `json:"shell_id"`
	Offset  int    `json:"offset,omitempty"`
	Limit   *int   `json:"limit,omitempty"`
}

type bashKillInput struct {
	ShellID string `json:"shell_id"`
}

func bashTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "bash",
			Description: "Run a shell command. Use background=true to keep it running and fetch output later via bash_output.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string","description":"The command to run"},"cwd":{"type":"string","description":"Working directory"},"timeout_seconds":{"type":"integer","description":"Timeout in seconds (default 30)"},"background":{"type":"boolean","description":"Run in background"}},"required":["cmd"]}`),
			Dangerous:   true,
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in bashInput
			if err := decodeToolInput(input, &in, "bash"); err != nil {
				return nil, err
			}

			cwd := in.Cwd
			if cwd == "" {
				cwd = tc.Workspace
			}

			entry, err := manager.start(tc.SessionID, in.Cmd, cwd)
			if err != nil {
				return nil, err
			}

			if in.Background {
				return &tool.Result{Text: fmt.Sprintf("started: %s", entry.id)}, nil
			}
			defer manager.removeIfSame(entry.id, entry)

			timeout := in.TimeoutSeconds
			if timeout <= 0 {
				timeout = defaultCommandTimeoutSeconds
			}

			select {
			case <-entry.done:
				// Command completed.
			case <-time.After(time.Duration(timeout) * time.Second):
				_ = manager.terminate(tc.SessionID, entry.id)
				return nil, fmt.Errorf("command timed out after %d seconds and was terminated", timeout)
			case <-ctx.Done():
				_ = manager.terminate(tc.SessionID, entry.id)
				return nil, ctx.Err()
			}

			output := foregroundOutputText(entry.output.String())
			if code, ok := entry.returnCodeValue(); ok && code != 0 {
				return nil, fmt.Errorf("command exited with code %d\noutput:\n%s", code, output)
			}

			return &tool.Result{Text: output}, nil
		},
	}
}

func bashOutputTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "bash.output",
			Description: "Read buffered output from a background shell, with optional offset/limit for incremental polling.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"shell_id":{"type":"string","description":"The shell ID returned by bash"},"offset":{"type":"integer","description":"Character offset to start reading from"},"limit":{"type":"integer","description":"Maximum characters to return"}},"required":["shell_id"]}`),
			Dangerous:   true,
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in bashOutputInput
			if err := decodeToolInput(input, &in, "bash.output"); err != nil {
				return nil, err
			}

			entry, err := manager.requireForSession(tc.SessionID, in.ShellID)
			if err != nil {
				return nil, err
			}

			chunk, nextOffset, total := pagedOutput(entry.output.String(), in.Offset, in.Limit)
			status, exitCode := shellStatusText(entry)

			text := fmt.Sprintf("id: %s\nstatus: %s\nexit_code: %s\nnext_offset: %d\noutput:\n%s",
				in.ShellID, status, exitCode, nextOffset, chunk)
			if status == "exited" && nextOffset >= total {
				manager.removeIfSame(entry.id, entry)
			}
			return &tool.Result{Text: text}, nil
		},
	}
}

func bashKillTool() *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "bash.kill",
			Description: "Terminate a background shell process.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"shell_id":{"type":"string","description":"The shell ID to terminate"}},"required":["shell_id"]}`),
			Dangerous:   true,
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			var in bashKillInput
			if err := decodeToolInput(input, &in, "bash.kill"); err != nil {
				return nil, err
			}

			entry, err := manager.requireForSession(tc.SessionID, in.ShellID)
			if err != nil {
				return nil, err
			}

			if err := manager.terminate(tc.SessionID, in.ShellID); err != nil {
				return nil, err
			}

			_, exitCode := shellStatusText(entry)

			text := fmt.Sprintf("id: %s\nstatus: terminated\nexit_code: %s", in.ShellID, exitCode)
			return &tool.Result{Text: text}, nil
		},
	}
}
