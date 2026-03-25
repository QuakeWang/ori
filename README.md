# Ori

`ori` is an intelligent operations and diagnostics agent for Apache Doris.

The name comes from `d-ori-s`.

It preserves the operator-facing local workflow behaviors that matter in practice:

- `AGENTS.md` is appended to the system prompt
- skills are discovered from project, legacy-project, global, and builtin roots
- tools keep dotted human-facing names such as `bash.output` and `doris.sql`
- session context is reconstructed from append-only JSONL events
- lines starting with `,` enter internal command mode

Default builds include Doris-specific tools and builtin diagnostic skills.

## Current Scope

Implemented today:

- `ori run`
- `ori chat`
- `ori tools`
- `ori skills`
- typed tool registry
- builtin skill discovery and activation
- JSONL-backed local session store
- Doris extension: `doris.ping` for connection check, diagnostics through builtin skills
- Doris builtin skills: `health-check`, `slow-query`, `schema-audit`, `explain-analyze`

## Quick Start

```bash
cd ori
cp .env.example .env
go build -o ori ./cmd/ori
./ori version
```

Run one-shot command mode without an API key:

```bash
./ori run ",help"
```

Read a file with explicit pagination:

```bash
./ori run ",fs.read path=README.md offset=0 limit=80"
```

Run a normal one-shot task:

```bash
./ori run "summarize this repo"
```

Start a local chat session:

```bash
./ori chat
```

List tools and skills:

```bash
./ori tools
./ori skills
```

## Configuration

See [.env.example](./.env.example) for the full set of supported variables.

Important settings:

- `ORI_MODEL` — model identifier, e.g. `openai:gpt-4o`
- `ORI_API_KEY`
- `ORI_API_BASE`
- `ORI_API_FORMAT` — `completion` (default) or `responses`
- `ORI_MAX_STEPS` — max agent loop iterations (default 50)
- `ORI_MAX_TOKENS` — max output tokens per LLM call (default 16384)
- `ORI_HOME` — state directory (default `~/.ori`)
- `ORI_CONTEXT_WINDOW_SIZE` — number of recent visible entries that keep full tool_result content; older results are truncated (default 30, 0 = disabled)
- `ORI_CONTEXT_MAX_TOOL_RESULT` — max characters for truncated tool_result content outside the window (default 300)
- `ORI_CONTEXT_MAX_TOOL_RESULT_IN_WINDOW` — max characters for tool_result inside the window; prevents oversized results from inflating context (default 8000, -1 = disabled)

Provider routing rules:

- `provider:model` always routes to the named provider directly.
- Unprefixed models use the global `ORI_API_KEY` when it is set.
- If there is no global `ORI_API_KEY` and exactly one provider-specific key is configured, Ori auto-promotes that provider as the default for unprefixed models.
- If multiple provider-specific keys are configured and there is no global `ORI_API_KEY`, unprefixed models are rejected at startup. Use explicit `provider:model` strings in that case.
- Fallback models should prefer explicit `provider:model` format for predictable routing.

Doris extension settings:

- `DORIS_FE_HOST`
- `DORIS_FE_PORT`
- `DORIS_USER`
- `DORIS_PASSWORD`
- `DORIS_DATABASE`
- `DORIS_CONNECT_TIMEOUT`
- `DORIS_QUERY_TIMEOUT`

File tool limits:

- `fs.read` reads regular text files only.
- `fs.read` uses streaming pagination and defaults to 200 lines when `limit` is omitted.
- `fs.read` rejects files larger than 10 MiB and truncates returned text at 64 KiB.
- `fs.edit` rejects files larger than 1 MiB and refuses binary files.

## Connect To Doris

The Doris extension connects to Doris FE over the MySQL protocol.

Minimal Doris configuration:

```env
DORIS_FE_HOST=127.0.0.1
DORIS_FE_PORT=9030
DORIS_USER=root
DORIS_PASSWORD=
DORIS_DATABASE=
DORIS_CONNECT_TIMEOUT=10
DORIS_QUERY_TIMEOUT=30
```

If your FE is remote, replace `DORIS_FE_HOST`, `DORIS_FE_PORT`, `DORIS_USER`, and `DORIS_PASSWORD` with your actual connection settings.

Quick connection check:

```bash
./ori run ",doris.ping"
```

Interactive Doris diagnostics:

```bash
./ori chat
```

## Project Layout

```text
ori/
├── cmd/ori
├── internal/agent
├── internal/app
├── internal/config
├── internal/doris
├── internal/llm
├── internal/session
├── internal/skill
├── internal/store
├── internal/tool
└── internal/ui
```

High-level boundaries:

- `internal/app`: compile-time assembly of core runtime plus optional extensions
- `internal/skill`: generic skill discovery and rendering
- `internal/doris`: Doris tools, SQL safety, formatting, and Doris-owned builtin skills
- `internal/ui`: local CLI commands only

## Runtime Notes

- binary name: `ori`, env prefix: `ORI_`
- default local session ID: `cli:local`
- dotted tool names in human-facing output (e.g. `doris.ping`)
- compile-time extension assembly instead of plugin loading

## Development

```bash
cd ori
go test ./...
go vet ./...
go build -o ori ./cmd/ori
./ori version
```
