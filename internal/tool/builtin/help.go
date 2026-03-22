package builtin

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/QuakeWang/ori/internal/tool"
)

type helpExample struct {
	name    string
	command string
}

var helpExamples = []helpExample{
	{name: "help", command: ",help"},
	{name: "skill", command: ",skill name=foo"},
	{name: "doris.ping", command: ",doris.ping"},
	{name: "doris.sql", command: ",doris.sql sql='SHOW FRONTENDS'"},
	{name: "doris.profile", command: ",doris.profile query_id=82d453a2105346fb-90eaedcefaca6992"},
	{name: "tape.info", command: ",tape.info"},
	{name: "tape.handoff", command: ",tape.handoff name=phase-1 summary='done'"},
	{name: "tape.anchors", command: ",tape.anchors"},
	{name: "tape.reset", command: ",tape.reset"},
	{name: "fs.read", command: ",fs.read path=README.md offset=0 limit=80"},
	{name: "fs.write", command: ",fs.write path=tmp.txt content='hello'"},
	{name: "fs.edit", command: ",fs.edit path=tmp.txt old=hello new=world"},
	{name: "web.fetch", command: ",web.fetch url=https://example.com"},
	{name: "bash", command: ",bash cmd='sleep 5' background=true"},
	{name: "bash.output", command: ",bash.output shell_id=bsh-<id>"},
	{name: "bash.kill", command: ",bash.kill shell_id=bsh-<id>"},
	{name: "subagent", command: ",subagent prompt='investigate the last command error'"},
	{name: "quit", command: ",quit"},
}

func helpTool(registry *tool.Registry) *tool.Tool {
	return &tool.Tool{
		Spec: tool.Spec{
			Name:        "help",
			Description: "Show a help message listing available commands.",
			Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Handler: func(ctx context.Context, tc *tool.Context, input json.RawMessage) (*tool.Result, error) {
			lines := []string{
				"Commands use ',' at line start.",
				"Known internal commands:",
			}

			for _, example := range helpExamples {
				if _, ok := registry.Get(example.name); ok {
					lines = append(lines, "  "+example.command)
				}
			}

			lines = append(lines,
				"Unknown commands show suggestions. Use ',bash cmd=...' for shell.",
			)
			text := strings.Join(lines, "\n")
			return &tool.Result{Text: text}, nil
		},
	}
}
