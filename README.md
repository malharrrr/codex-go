# codex-go

A minimal agentic coding harness in Go, built for learning purposes.  
It mirrors the architecture of [openai/codex](https://github.com/openai/codex) but in ~600 lines of readable Go.

## The agent loop

```
User types a task
       │
       ▼
loop/agent.go: Agent.Run()
       │
       ├─ Build messages: [system] [env_context] [history...]
       │
       ├─ POST /v1/chat/completions  ◄──── model thinks
       │
       ├─ Response has tool_calls?
       │    YES → dispatch each tool (shell / read_file / write_file / list_dir)
       │          append tool results to history
       │          loop back ↑
       │
       └─ Response is plain text → return to user, done
```

## Quickstart

```bash
export OPENAI_API_KEY=sk-...

# Build
go build -o codex-go .

# Interactive REPL (workspace = current directory)
./codex-go -workspace /path/to/your/project -verbose

# Single task, no prompts
./codex-go -task "List all Go files and count lines" -auto-approve

# Use a smarter model
./codex-go -model gpt-4o -workspace ./myproject
```

## Controlling the agent with AGENTS.md

Drop an `AGENTS.md` file in your workspace root. It's injected into the system
prompt on every turn (mirrors Codex's `project_doc.rs`).

```markdown
# Project conventions
- Always use `go test ./...` to run tests, never `go test .`
- Never modify files in the `vendor/` directory
- Use `goimports` after writing any Go file
```

## Extending it

**Add a new tool:**
1. Add a `ToolSpec` entry in `tools/spec.go`
2. Add a `case "your_tool":` in `tools/handlers.go`

**Add MCP support:**
- Start an MCP server subprocess on session init
- Fetch its tool list and merge into `tools.All()`
- Route calls to it in `Dispatcher.Dispatch()`

**Add streaming output:**
- Set `Stream: true` in `openAIRequest`
- Use `StreamingPrinter` in `loop/agent.go` to print tokens as they arrive
- Mirrors `codex-api/src/sse/responses.rs` in Codex

**Add context compaction:**
- Track token usage from `usage` field in API responses
- When `total_tokens > threshold`, summarize history and replace it
- Mirrors `run_auto_compact` in `codex-rs/core/src/codex.rs`
