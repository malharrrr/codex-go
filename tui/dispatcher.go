package tui

import (
	"context"
	"strings"

	"codex-go/tools"
)

type UIDispatcher struct {
	inner *tools.Dispatcher
	app   *App
}

func NewUIDispatcher(d *tools.Dispatcher, app *App) *UIDispatcher {
	return &UIDispatcher{inner: d, app: app}
}

func (u *UIDispatcher) Dispatch(ctx context.Context, call tools.ToolCall) tools.ToolResult {
	preview := previewArgs(call)
	u.app.AppendToolCall(call.Name, preview)

	result := u.inner.Dispatch(ctx, call)

	u.app.AppendToolResult(call.Name, result.Output, result.IsError)
	return result
}

func previewArgs(call tools.ToolCall) string {
	raw := string(call.Args)
	switch call.Name {
	case "shell":
		return extractJSONString(raw, "command")
	case "read_file", "write_file":
		return extractJSONString(raw, "path")
	case "list_dir":
		if p := extractJSONString(raw, "path"); p != "" {
			return p
		}
		return "."
	}
	if len(raw) > 100 {
		return raw[:100] + "…"
	}
	return raw
}

func extractJSONString(json, key string) string {
	needle := `"` + key + `"`
	idx := strings.Index(json, needle)
	if idx < 0 {
		return ""
	}
	rest := json[idx+len(needle):]
	for i, c := range rest {
		if c == '"' {
			val := rest[i+1:]
			end := strings.IndexByte(val, '"')
			if end < 0 {
				return val
			}
			return val[:end]
		}
	}
	return ""
}