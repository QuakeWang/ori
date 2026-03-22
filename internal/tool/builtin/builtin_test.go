package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/QuakeWang/ori/internal/llm"
	"github.com/QuakeWang/ori/internal/session"
	"github.com/QuakeWang/ori/internal/skill"
	"github.com/QuakeWang/ori/internal/store"
	"github.com/QuakeWang/ori/internal/tool"
)

// ------------------------------------------------------------------ RegisterCore

func TestRegisterCore(t *testing.T) {
	r := tool.NewRegistry()
	err := RegisterCore(r, &skill.Service{})
	require.NoError(t, err)

	expectedNames := []string{
		"help", "bash", "bash.output", "bash.kill",
		"fs.read", "fs.write", "fs.edit", "web.fetch", "quit",
		"skill", "tape.info", "tape.reset",
		"tape.handoff", "tape.anchors", "subagent",
	}
	for _, name := range expectedNames {
		_, ok := r.Get(name)
		assert.True(t, ok, "tool %q should be registered", name)
	}

	_, ok := r.Get("doris.ping")
	assert.False(t, ok, "doris tools should be assembled outside builtin registration")
	_, ok = r.Get("doris.sql")
	assert.False(t, ok, "doris tools should be assembled outside builtin registration")
}

// ------------------------------------------------------------------ help tool

func TestHelpTool_WithRegisteredDorisCommands(t *testing.T) {
	r := tool.NewRegistry()
	require.NoError(t, RegisterCore(r, &skill.Service{}))
	require.NoError(t, r.Register(&tool.Tool{Spec: tool.Spec{Name: "doris.ping"}}))
	require.NoError(t, r.Register(&tool.Tool{Spec: tool.Spec{Name: "doris.sql"}}))
	require.NoError(t, r.Register(&tool.Tool{Spec: tool.Spec{Name: "doris.profile"}}))

	h, ok := r.Get("help")
	require.True(t, ok)
	result, err := h.Handler(context.Background(), &tool.Context{}, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "Known internal commands:")
	assert.Contains(t, result.Text, ",doris.ping")
	assert.Contains(t, result.Text, ",doris.sql sql='SHOW FRONTENDS'")
	assert.Contains(t, result.Text, ",doris.profile query_id=")
	assert.Contains(t, result.Text, ",bash.output")
	assert.Contains(t, result.Text, ",fs.read")
	assert.Contains(t, result.Text, ",tape.info")
	assert.Contains(t, result.Text, ",tape.reset")
}

func TestHelpTool_WithoutRegisteredDorisCommands(t *testing.T) {
	r := tool.NewRegistry()
	require.NoError(t, RegisterCore(r, &skill.Service{}))

	h, ok := r.Get("help")
	require.True(t, ok)
	result, err := h.Handler(context.Background(), &tool.Context{}, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "Known internal commands:")
	assert.NotContains(t, result.Text, ",doris.ping")
	assert.NotContains(t, result.Text, ",doris.sql")
	assert.NotContains(t, result.Text, ",doris.profile")
	assert.Contains(t, result.Text, ",bash.output")
	assert.Contains(t, result.Text, ",fs.read")
	assert.Contains(t, result.Text, ",tape.info")
	assert.Contains(t, result.Text, ",tape.reset")
}

// ------------------------------------------------------------------ bash tool

func TestBashTool_SimpleCommand(t *testing.T) {
	b := bashTool()
	input := json.RawMessage(`{"cmd":"echo hello"}`)
	result, err := b.Handler(context.Background(), &tool.Context{}, input)
	require.NoError(t, err)
	assert.Equal(t, "hello", result.Text)
}

func TestBashTool_NoOutputUsesPlaceholder(t *testing.T) {
	b := bashTool()
	result, err := b.Handler(context.Background(), &tool.Context{}, json.RawMessage(`{"cmd":":"}`))
	require.NoError(t, err)
	assert.Equal(t, "(no output)", result.Text)
}

func TestBashTool_ForegroundCommandIsCleanedUp(t *testing.T) {
	baseline := manager.count()

	b := bashTool()
	input := json.RawMessage(`{"cmd":"echo cleanup-check"}`)
	result, err := b.Handler(context.Background(), &tool.Context{}, input)
	require.NoError(t, err)
	assert.Equal(t, "cleanup-check", result.Text)

	assert.Equal(t, baseline, manager.count(), "foreground command should not remain in shell manager")
}

func TestBashTool_FailedCommand(t *testing.T) {
	b := bashTool()
	input := json.RawMessage(`{"cmd":"exit 1"}`)
	_, err := b.Handler(context.Background(), &tool.Context{}, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 1")
}

func TestBashTool_Background(t *testing.T) {
	b := bashTool()
	input := json.RawMessage(`{"cmd":"echo bg","background":true}`)
	result, err := b.Handler(context.Background(), &tool.Context{}, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "started: bsh-")
	manager.remove(strings.TrimPrefix(result.Text, "started: "))
}

func TestBashTool_WithCwd(t *testing.T) {
	b := bashTool()
	input := json.RawMessage(`{"cmd":"pwd","cwd":"/tmp"}`)
	result, err := b.Handler(context.Background(), &tool.Context{}, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "tmp")
}

func TestBashTool_Timeout(t *testing.T) {
	b := bashTool()
	input := json.RawMessage(`{"cmd":"sleep 10","timeout_seconds":1}`)
	_, err := b.Handler(context.Background(), &tool.Context{}, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestBashTool_ContextCancel(t *testing.T) {
	b := bashTool()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	input := json.RawMessage(`{"cmd":"sleep 10"}`)
	_, err := b.Handler(ctx, &tool.Context{}, input)
	assert.Error(t, err)
}

// ------------------------------------------------------------------ bash.output tool

func TestBashOutputTool(t *testing.T) {
	// Start a background command.
	b := bashTool()
	tc := &tool.Context{SessionID: "sess-output"}
	input := json.RawMessage(`{"cmd":"echo output-test","background":true}`)
	result, err := b.Handler(context.Background(), tc, input)
	require.NoError(t, err)

	// Extract shell ID.
	shellID := result.Text[len("started: "):]
	time.Sleep(200 * time.Millisecond) // wait for command to finish

	// Read output.
	bo := bashOutputTool()
	outputInput, _ := json.Marshal(map[string]string{"shell_id": shellID})
	result, err = bo.Handler(context.Background(), tc, outputInput)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "output-test")
	assert.Contains(t, result.Text, "status: exited")
	_, ok := manager.get(shellID)
	assert.False(t, ok, "exited shell should be removed after final output is read")
}

func TestBashOutputTool_EmptyOutputUsesPlaceholder(t *testing.T) {
	b := bashTool()
	tc := &tool.Context{SessionID: "sess-empty-output"}
	result, err := b.Handler(context.Background(), tc, json.RawMessage(`{"cmd":":","background":true}`))
	require.NoError(t, err)

	shellID := result.Text[len("started: "):]
	time.Sleep(200 * time.Millisecond)

	bo := bashOutputTool()
	result, err = bo.Handler(context.Background(), tc, mustJSON(t, map[string]any{"shell_id": shellID}))
	require.NoError(t, err)
	assert.Contains(t, result.Text, "status: exited")
	assert.Contains(t, result.Text, "exit_code: 0")
	assert.Contains(t, result.Text, "output:\n(no output)")
}

func TestBashOutputTool_ExitedPartialReadCanContinue(t *testing.T) {
	b := bashTool()
	tc := &tool.Context{SessionID: "sess-partial-exit"}
	input := json.RawMessage(`{"cmd":"printf 'abcdef'","background":true}`)
	result, err := b.Handler(context.Background(), tc, input)
	require.NoError(t, err)

	shellID := result.Text[len("started: "):]
	time.Sleep(200 * time.Millisecond)

	bo := bashOutputTool()

	first, err := bo.Handler(context.Background(), tc, mustJSON(t, map[string]any{
		"shell_id": shellID,
		"offset":   0,
		"limit":    3,
	}))
	require.NoError(t, err)
	assert.Contains(t, first.Text, "status: exited")
	assert.Contains(t, first.Text, "next_offset: 3")
	assert.Contains(t, first.Text, "output:\nabc")
	_, ok := manager.get(shellID)
	assert.True(t, ok, "partial read must keep exited shell available for follow-up polling")

	second, err := bo.Handler(context.Background(), tc, mustJSON(t, map[string]any{
		"shell_id": shellID,
		"offset":   3,
		"limit":    3,
	}))
	require.NoError(t, err)
	assert.Contains(t, second.Text, "status: exited")
	assert.Contains(t, second.Text, "next_offset: 6")
	assert.Contains(t, second.Text, "output:\ndef")

	_, ok = manager.get(shellID)
	assert.False(t, ok, "shell should be removed after the exited output has been fully consumed")
}

func TestBashOutputTool_UTF8OffsetsAreCharacterBased(t *testing.T) {
	b := bashTool()
	tc := &tool.Context{SessionID: "sess-utf8"}
	result, err := b.Handler(context.Background(), tc, json.RawMessage(`{"cmd":"printf '你好世界'","background":true}`))
	require.NoError(t, err)

	shellID := result.Text[len("started: "):]
	time.Sleep(200 * time.Millisecond)

	bo := bashOutputTool()
	first, err := bo.Handler(context.Background(), tc, mustJSON(t, map[string]any{
		"shell_id": shellID,
		"offset":   0,
		"limit":    2,
	}))
	require.NoError(t, err)
	assert.Contains(t, first.Text, "next_offset: 2")
	assert.Contains(t, first.Text, "output:\n你好")

	second, err := bo.Handler(context.Background(), tc, mustJSON(t, map[string]any{
		"shell_id": shellID,
		"offset":   2,
		"limit":    2,
	}))
	require.NoError(t, err)
	assert.Contains(t, second.Text, "next_offset: 4")
	assert.Contains(t, second.Text, "output:\n世界")
}

// TestBashOutputTool_ConcurrentSafety exercises the syncBuffer under
// concurrent read/write. Run with `go test -race` to detect data races.
func TestBashOutputTool_ConcurrentSafety(t *testing.T) {
	b := bashTool()
	// Produce output over ~500ms so reads overlap with writes.
	tc := &tool.Context{SessionID: "sess-concurrent"}
	input := json.RawMessage(`{"cmd":"for i in $(seq 1 50); do echo line-$i; sleep 0.01; done","background":true}`)
	result, err := b.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	shellID := result.Text[len("started: "):]

	bo := bashOutputTool()
	outputInput, _ := json.Marshal(map[string]string{"shell_id": shellID})

	// Concurrent readers while the process is writing.
	var wg sync.WaitGroup
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				_, _ = bo.Handler(context.Background(), tc, outputInput)
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	wg.Wait()

	// Wait for process to finish, then verify all output is present.
	entry, ok := manager.get(shellID)
	require.True(t, ok)
	<-entry.done

	final, err := bo.Handler(context.Background(), tc, outputInput)
	require.NoError(t, err)
	assert.Contains(t, final.Text, "line-50")
	assert.Contains(t, final.Text, "status: exited")
}

func TestBashOutputTool_NotFound(t *testing.T) {
	bo := bashOutputTool()
	input := json.RawMessage(`{"shell_id":"bsh-nonexistent"}`)
	_, err := bo.Handler(context.Background(), &tool.Context{}, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ------------------------------------------------------------------ bash.kill tool

func TestBashKillTool(t *testing.T) {
	// Start a long-running background command.
	b := bashTool()
	tc := &tool.Context{SessionID: "sess-kill"}
	input := json.RawMessage(`{"cmd":"sleep 60","background":true}`)
	result, err := b.Handler(context.Background(), tc, input)
	require.NoError(t, err)

	shellID := result.Text[len("started: "):]

	// Kill it.
	bk := bashKillTool()
	killInput, _ := json.Marshal(map[string]string{"shell_id": shellID})
	result, err = bk.Handler(context.Background(), tc, killInput)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "terminated")
	_, ok := manager.get(shellID)
	assert.False(t, ok, "terminated shell should be removed from manager")
}

func TestBashOutputTool_SessionIsolation(t *testing.T) {
	b := bashTool()
	owner := &tool.Context{SessionID: "sess-owner"}
	other := &tool.Context{SessionID: "sess-other"}

	result, err := b.Handler(context.Background(), owner, json.RawMessage(`{"cmd":"echo secret && sleep 60","background":true}`))
	require.NoError(t, err)
	shellID := strings.TrimPrefix(result.Text, "started: ")

	t.Cleanup(func() {
		_, _ = bashKillTool().Handler(context.Background(), owner, mustJSON(t, map[string]string{"shell_id": shellID}))
	})

	_, err = bashOutputTool().Handler(context.Background(), other, mustJSON(t, map[string]string{"shell_id": shellID}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestBashKillTool_SessionIsolation(t *testing.T) {
	b := bashTool()
	owner := &tool.Context{SessionID: "sess-owner-kill"}
	other := &tool.Context{SessionID: "sess-other-kill"}

	result, err := b.Handler(context.Background(), owner, json.RawMessage(`{"cmd":"sleep 60","background":true}`))
	require.NoError(t, err)
	shellID := strings.TrimPrefix(result.Text, "started: ")

	_, err = bashKillTool().Handler(context.Background(), other, mustJSON(t, map[string]string{"shell_id": shellID}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, ok := manager.get(shellID)
	assert.True(t, ok, "unauthorized kill must not remove the owner shell")

	_, err = bashKillTool().Handler(context.Background(), owner, mustJSON(t, map[string]string{"shell_id": shellID}))
	require.NoError(t, err)
}

func TestBashTool_CompletedBackgroundShellExpires(t *testing.T) {
	previousRetention := completedShellRetention()
	setCompletedShellRetention(20 * time.Millisecond)
	t.Cleanup(func() {
		setCompletedShellRetention(previousRetention)
	})

	b := bashTool()
	tc := &tool.Context{SessionID: "sess-expire"}
	result, err := b.Handler(context.Background(), tc, json.RawMessage(`{"cmd":"echo expire-me","background":true}`))
	require.NoError(t, err)
	shellID := strings.TrimPrefix(result.Text, "started: ")

	require.Eventually(t, func() bool {
		_, ok := manager.get(shellID)
		return !ok
	}, time.Second, 10*time.Millisecond, "completed background shell should not stay in manager forever")
}

func TestBashKillTool_NotFound(t *testing.T) {
	bk := bashKillTool()
	input := json.RawMessage(`{"shell_id":"bsh-nonexistent"}`)
	_, err := bk.Handler(context.Background(), &tool.Context{}, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ------------------------------------------------------------------ fs.read tool

func TestFsReadTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(path, []byte("line0\nline1\nline2\nline3"), 0o644))

	f := fsReadTool()
	tc := &tool.Context{Workspace: dir}

	input := json.RawMessage(`{"path":"test.txt"}`)
	result, err := f.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "line0")
	assert.Contains(t, result.Text, "line3")
}

func TestFsReadTool_OffsetLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(path, []byte("line0\nline1\nline2\nline3"), 0o644))

	f := fsReadTool()
	tc := &tool.Context{Workspace: dir}

	input := json.RawMessage(`{"path":"test.txt","offset":1,"limit":2}`)
	result, err := f.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Equal(t, "line1\nline2", result.Text)
}

func TestFsReadTool_RelativePath(t *testing.T) {
	dir := t.TempDir()
	subPath := filepath.Join(dir, "subdir", "test.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(subPath), 0o755))
	require.NoError(t, os.WriteFile(subPath, []byte("content"), 0o644))

	f := fsReadTool()
	tc := &tool.Context{Workspace: dir}

	input := json.RawMessage(`{"path":"subdir/test.txt"}`)
	result, err := f.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Equal(t, "content", result.Text)
}

func TestFsReadTool_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	f := fsReadTool()
	tc := &tool.Context{Workspace: dir}

	input := json.RawMessage(`{"path":"nonexistent.txt"}`)
	_, err := f.Handler(context.Background(), tc, input)
	assert.Error(t, err)
}

func TestFsReadTool_LargeFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	// Write a file just over 10 MiB (the maxReadSize for fs.read).
	data := make([]byte, (10<<20)+1)
	for i := range data {
		data[i] = 'x'
	}
	require.NoError(t, os.WriteFile(path, data, 0o644))

	f := fsReadTool()
	tc := &tool.Context{Workspace: dir}
	input := json.RawMessage(`{"path":"huge.txt"}`)
	_, err := f.Handler(context.Background(), tc, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}

func TestFsReadTool_BinaryRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	data := []byte("hello\x00world")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	f := fsReadTool()
	tc := &tool.Context{Workspace: dir}
	input := json.RawMessage(`{"path":"binary.bin"}`)
	_, err := f.Handler(context.Background(), tc, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "binary file")
}

func TestFsReadTool_DefaultLimitTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.txt")
	// Generate 300 lines.
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644))

	f := fsReadTool()
	tc := &tool.Context{Workspace: dir}
	input := json.RawMessage(`{"path":"long.txt"}`)
	result, err := f.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.True(t, result.Truncated)
	assert.Contains(t, result.Text, "[truncated:")
	// Should contain line0 but not line299.
	assert.Contains(t, result.Text, "line0")
	assert.NotContains(t, result.Text, "line299")
}

func TestFsReadTool_ByteLimitTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wide.txt")
	// Generate a file with a few very long lines that exceed maxResultBytes (64 KiB)
	// when taken together. Each line is 20 KiB, so 5 lines = 100 KiB > 64 KiB.
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, strings.Repeat("A", 20*1024))
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644))

	f := fsReadTool()
	tc := &tool.Context{Workspace: dir}
	// Explicit limit of 5 lines — but byte cap should kick in before all 5 are returned.
	input := json.RawMessage(`{"path":"wide.txt","limit":5}`)
	result, err := f.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.True(t, result.Truncated)
	assert.Contains(t, result.Text, "byte limit")
	// Should have fewer than 5 lines worth of content (64 KiB / 20 KiB ≈ 3 lines).
	assert.Less(t, len(result.Text), 5*20*1024)
}

func TestFsEditTool_LargeFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	// Write a file just over 1 MiB (maxEditSize).
	data := make([]byte, (1<<20)+1)
	for i := range data {
		data[i] = 'x'
	}
	require.NoError(t, os.WriteFile(path, data, 0o644))

	f := fsEditTool()
	tc := &tool.Context{Workspace: dir}
	input := json.RawMessage(`{"path":"huge.txt","old":"xxx","new":"yyy"}`)
	_, err := f.Handler(context.Background(), tc, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}

func TestFsEditTool_BinaryRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	data := []byte("hello\x00world")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	f := fsEditTool()
	tc := &tool.Context{Workspace: dir}
	input := json.RawMessage(`{"path":"binary.bin","old":"hello","new":"bye"}`)
	_, err := f.Handler(context.Background(), tc, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "binary file")
}

// ------------------------------------------------------------------ fs.write tool

func TestFsWriteTool(t *testing.T) {
	dir := t.TempDir()
	f := fsWriteTool()
	tc := &tool.Context{Workspace: dir}

	input := json.RawMessage(`{"path":"out.txt","content":"hello world"}`)
	result, err := f.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "wrote")

	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))
}

// ------------------------------------------------------------------ fs.edit tool

func TestFsEditTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello world"), 0o644))

	f := fsEditTool()
	tc := &tool.Context{Workspace: dir}

	input := json.RawMessage(`{"path":"edit.txt","old":"hello","new":"goodbye"}`)
	result, err := f.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "edited")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "goodbye world", string(data))
}

func TestFsEditTool_StartLineLimitsReplacementScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello first\nhello second"), 0o644))

	f := fsEditTool()
	tc := &tool.Context{Workspace: dir}

	input := json.RawMessage(`{"path":"edit.txt","old":"hello","new":"goodbye","start":1}`)
	result, err := f.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "edited")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello first\ngoodbye second", string(data))
}

func TestFsEditTool_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello world"), 0o644))

	f := fsEditTool()
	tc := &tool.Context{Workspace: dir}

	input := json.RawMessage(`{"path":"edit.txt","old":"nonexistent","new":"x"}`)
	_, err := f.Handler(context.Background(), tc, input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ------------------------------------------------------------------ workspace boundary

func TestResolvePath_WithinWorkspace(t *testing.T) {
	dir := t.TempDir()
	p, err := resolvePath(dir, "subdir/file.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "subdir/file.txt"), p)
}

func TestResolvePath_AbsoluteWithinWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inside.txt")
	p, err := resolvePath(dir, path)
	require.NoError(t, err)
	assert.Equal(t, path, p)
}

func TestResolvePath_AbsoluteEscapesWorkspace(t *testing.T) {
	dir := t.TempDir()
	_, err := resolvePath(dir, "/etc/passwd")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace boundary")
}

func TestResolvePath_RelativeEscapesWorkspace(t *testing.T) {
	dir := t.TempDir()
	_, err := resolvePath(dir, "../../etc/passwd")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace boundary")
}

func TestResolvePath_NoWorkspace(t *testing.T) {
	_, err := resolvePath("", "file.txt")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed without a workspace")
}

// ------------------------------------------------------------------ quit tool

func TestQuitTool(t *testing.T) {
	q := quitTool()
	result, err := q.Handler(context.Background(), &tool.Context{}, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "stopped")
	assert.Equal(t, true, result.Meta["quit"])
}

// ------------------------------------------------------------------ tape tools

func TestTapeTools_AppearInSchemas(t *testing.T) {
	r := tool.NewRegistry()
	require.NoError(t, RegisterCore(r, &skill.Service{}))

	// Default filter should include tape tools.
	schemas := r.Schemas(tool.Filter{})
	schemaNames := make([]string, 0, len(schemas))
	for _, s := range schemas {
		schemaNames = append(schemaNames, s.Name)
	}
	assert.Contains(t, schemaNames, "tape_info")
	assert.Contains(t, schemaNames, "tape_handoff")
	assert.Contains(t, schemaNames, "tape_anchors")
	// Real tools should appear (minus Dangerous ones).
	assert.Contains(t, schemaNames, "help")
	assert.Contains(t, schemaNames, "skill")
	assert.NotContains(t, schemaNames, "bash")
	assert.NotContains(t, schemaNames, "bash_output")
	assert.NotContains(t, schemaNames, "bash_kill")
	assert.NotContains(t, schemaNames, "web_fetch")
	assert.NotContains(t, schemaNames, "doris_ping")
	assert.NotContains(t, schemaNames, "doris_sql")
}

func newTapeTestContext(t *testing.T) *tool.Context {
	t.Helper()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	st, err := store.NewJSONLStore(storeDir, workspace)
	require.NoError(t, err)

	state := session.NewState("sess-1", workspace)
	tc := &tool.Context{
		SessionID: "sess-1",
		Workspace: workspace,
		State:     state,
		Store:     st,
	}

	require.NoError(t, st.AddAnchor(tc.SessionID, "session/start", nil))
	require.NoError(t, st.Append(tc.SessionID, store.NewUserEntry(storeTestMessage("hello tape"))))
	require.NoError(t, st.Append(tc.SessionID, store.NewAssistantEntry(storeTestAssistant("hello back"), "stop", storeTestUsage())))
	return tc
}

func storeTestMessage(content string) llm.Message {
	return llm.Message{Role: "user", Content: content}
}

func storeTestAssistant(content string) llm.Message {
	return llm.Message{Role: "assistant", Content: content}
}

func storeTestUsage() llm.Usage {
	return llm.Usage{TotalTokens: 12}
}

func TestTapeInfoTool(t *testing.T) {
	tc := newTapeTestContext(t)
	result, err := tapeInfoTool().Handler(context.Background(), tc, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "name: sess-1")
	assert.Contains(t, result.Text, "anchors: 1")
	assert.Contains(t, result.Text, "entries: 3")
}

func TestTapeHandoffAndAnchorsTools(t *testing.T) {
	tc := newTapeTestContext(t)
	input := json.RawMessage(`{"name":"phase-1","summary":"done"}`)
	result, err := tapeHandoffTool().Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "anchor added: phase-1")

	anchors, err := tapeAnchorsTool().Handler(context.Background(), tc, nil)
	require.NoError(t, err)
	assert.Contains(t, anchors.Text, "- session/start")
	assert.Contains(t, anchors.Text, "- phase-1")
}

func TestTapeHandoffTool_InvalidInput(t *testing.T) {
	tc := newTapeTestContext(t)
	_, err := tapeHandoffTool().Handler(context.Background(), tc, json.RawMessage(`{"name":`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid tape.handoff input")
}

func TestTapeResetTool(t *testing.T) {
	tc := newTapeTestContext(t)
	tc.State.Extras["budget"] = json.RawMessage(`100`)
	result, err := tapeResetTool().Handler(context.Background(), tc, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "tape reset")
	assert.Equal(t, true, result.Meta["skip_state_save"])

	entries, err := tc.Store.List(tc.SessionID, 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Empty(t, tc.State.ActivatedSkills)
	assert.Empty(t, tc.State.Extras)
	require.NotNil(t, tc.State.Extras)
}

// ------------------------------------------------------------------ symlink boundary

func TestResolvePath_SymlinkEscapesWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()

	// Create a file outside workspace.
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("secret"), 0o644))

	// Create a symlink inside workspace pointing outside.
	symlinkPath := filepath.Join(workspace, "escape-link")
	require.NoError(t, os.Symlink(outsideDir, symlinkPath))

	// Accessing via symlink should be rejected.
	_, err := resolvePath(workspace, "escape-link/secret.txt")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "escapes workspace boundary")
}

// ------------------------------------------------------------------ skill tool behavior

func TestSkillTool_Activation(t *testing.T) {
	workspace := t.TempDir()
	setupTestSkill(t, workspace, "test-skill",
		"---\nname: test-skill\ndescription: A test\nmax_steps: 5\nblocked_sql:\n  - SELECT\\s+\\*\n---\nInstructions here.")

	svc := &skill.Service{}
	st := skillTool(svc)
	state := &session.State{ActivatedSkills: nil}
	tc := &tool.Context{Workspace: workspace, State: state}

	input := json.RawMessage(`{"name":"test-skill"}`)
	result, err := st.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "Instructions here.")

	// Activation should be recorded.
	act, ok := state.ActivatedSkills["test-skill"]
	require.True(t, ok, "skill should be activated")
	assert.Equal(t, "test-skill", act.Name)

	// max_steps should be propagated to MaxStepsOverride.
	require.NotNil(t, act.MaxStepsOverride)
	assert.Equal(t, 5, *act.MaxStepsOverride)
	require.Contains(t, act.Metadata, "blocked_sql")
	assert.JSONEq(t, `["SELECT\\s+\\*"]`, string(skill.ActiveBlockedSQL(state)))
}

func TestSkillTool_BlockedSQLRuntimeStateOverridesPreviousSkill(t *testing.T) {
	workspace := t.TempDir()
	setupTestSkill(t, workspace, "health-check",
		"---\nname: health-check\ndescription: Health\nblocked_sql:\n  - SHOW\\s+CREATE\\s+TABLE\n---\nBody.")
	setupTestSkill(t, workspace, "schema-audit",
		"---\nname: schema-audit\ndescription: Schema\n---\nBody.")

	svc := &skill.Service{}
	st := skillTool(svc)
	state := session.NewState("sess-1", workspace)
	tc := &tool.Context{Workspace: workspace, State: state}

	_, err := st.Handler(context.Background(), tc, json.RawMessage(`{"name":"health-check"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `["SHOW\\s+CREATE\\s+TABLE"]`, string(skill.ActiveBlockedSQL(state)))

	_, err = st.Handler(context.Background(), tc, json.RawMessage(`{"name":"schema-audit"}`))
	require.NoError(t, err)
	assert.Nil(t, skill.ActiveBlockedSQL(state))
}

func TestSkillTool_AllowedSkillsFilter(t *testing.T) {
	workspace := t.TempDir()
	setupTestSkill(t, workspace, "blocked-skill",
		"---\nname: blocked-skill\ndescription: Blocked\n---\nBody.")

	svc := &skill.Service{}
	st := skillTool(svc)
	state := &session.State{
		AllowedSkills: map[string]bool{"other-skill": true},
	}
	tc := &tool.Context{Workspace: workspace, State: state}

	input := json.RawMessage(`{"name":"blocked-skill"}`)
	result, err := st.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "not allowed")
}

func TestSkillTool_NotFound(t *testing.T) {
	workspace := t.TempDir()
	svc := &skill.Service{}
	st := skillTool(svc)
	tc := &tool.Context{Workspace: workspace}

	input := json.RawMessage(`{"name":"nonexistent"}`)
	result, err := st.Handler(context.Background(), tc, input)
	require.NoError(t, err)
	assert.Contains(t, result.Text, "no such skill")
}

func TestSkillTool_EmptyName(t *testing.T) {
	workspace := t.TempDir()
	svc := &skill.Service{}
	st := skillTool(svc)
	tc := &tool.Context{Workspace: workspace}

	_, err := st.Handler(context.Background(), tc, json.RawMessage(`{"name":"   "}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skill name is required")
}

func setupTestSkill(t *testing.T, workspace, name, content string) {
	t.Helper()
	dir := filepath.Join(workspace, ".agents", "skills", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}
