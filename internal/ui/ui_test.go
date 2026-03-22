package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestRootCmd_HasSubcommands(t *testing.T) {
	root := RootCmd()
	names := make([]string, 0, len(root.Commands()))
	for _, cmd := range root.Commands() {
		names = append(names, cmd.Name())
	}
	assert.Contains(t, names, "run")
	assert.Contains(t, names, "chat")
	assert.Contains(t, names, "tools")
	assert.Contains(t, names, "skills")
}

func TestRenderer_Output_Assistant(t *testing.T) {
	var out, errOut bytes.Buffer
	r := NewRenderer(&out, &errOut)
	r.Output("Assistant", "Hello **world**")

	got := out.String()
	assert.Contains(t, got, "Assistant")
	assert.NotEmpty(t, got)
	assert.Empty(t, errOut.String())
}

func TestRenderer_Output_Error(t *testing.T) {
	var out, errOut bytes.Buffer
	r := NewRenderer(&out, &errOut)
	r.Output("Error", "something broke")

	assert.Empty(t, out.String())
	assert.Contains(t, errOut.String(), "something broke")
}

func TestRenderer_Output_EmptyBody(t *testing.T) {
	var out, errOut bytes.Buffer
	r := NewRenderer(&out, &errOut)
	r.Output("Info", "   ")

	assert.Empty(t, out.String())
	assert.Empty(t, errOut.String())
}

func TestRenderer_Table(t *testing.T) {
	var out, errOut bytes.Buffer
	r := NewRenderer(&out, &errOut)
	r.Table(
		[]string{"NAME", "VALUE"},
		[][]string{{"foo", "bar"}, {"baz", "qux"}},
	)

	got := out.String()
	assert.Contains(t, got, "NAME")
	assert.Contains(t, got, "foo")
	assert.Contains(t, got, "baz")
}

func TestSessionID_Default(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	addSessionFlags(cmd)
	_ = cmd.ParseFlags([]string{})
	assert.Equal(t, "cli:local", sessionID(cmd))
}

func TestSessionID_Explicit(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	addSessionFlags(cmd)
	_ = cmd.ParseFlags([]string{"--session-id", "custom"})
	assert.Equal(t, "custom", sessionID(cmd))
}

func TestWorkspaceFromCmd_DefaultsToCwd(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("workspace", "", "workspace directory")

	wd, err := os.Getwd()
	assert.NoError(t, err)

	temp := t.TempDir()
	assert.NoError(t, os.Chdir(temp))
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	assert.Equal(t, canonicalWorkspace(temp), workspaceFromCmd(cmd))
}

func TestWorkspaceFromCmd_ExplicitFlag(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("workspace", "", "workspace directory")

	target := t.TempDir()
	_ = cmd.ParseFlags([]string{"--workspace", target})

	assert.Equal(t, canonicalWorkspace(target), workspaceFromCmd(cmd))
}

func TestAddEphemeralFlag_DefaultFalse(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	addEphemeralFlag(cmd)
	_ = cmd.ParseFlags([]string{})

	value, err := cmd.Flags().GetBool("ephemeral")
	assert.NoError(t, err)
	assert.False(t, value)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", truncate("short", 10))
	assert.Equal(t, "1234567...", truncate("1234567890ABC", 10))
}

func TestSessionView_Formatting(t *testing.T) {
	view := SessionView{
		SessionID: "test:1", Workspace: "/tmp", Model: "gpt-4",
		Entries: 5, Anchors: 1, LastAnchor: "start", LastTokenUsage: 100,
	}
	details := formatSessionDetails(view)
	assert.Contains(t, details, "test:1")
	assert.Contains(t, details, "gpt-4")
	assert.Contains(t, details, "start")
}

func TestHistoryPath_EmptyHome(t *testing.T) {
	assert.Equal(t, "", historyPath("", "/workspace"))
	assert.Equal(t, "", historyPath("  ", "/workspace"))
}

func TestHistoryPath_DeterministicHash(t *testing.T) {
	p1 := historyPath(t.TempDir(), "/workspace/a")
	p2 := historyPath(t.TempDir(), "/workspace/a")
	// Same workspace should produce same filename.
	base1 := p1[strings.LastIndex(p1, "/")+1:]
	base2 := p2[strings.LastIndex(p2, "/")+1:]
	assert.Equal(t, base1, base2)
}
