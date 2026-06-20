package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"codex-go/sandbox"
)

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`
}

type Dispatcher struct {
	WorkspaceRoot  string
	DefaultTimeout time.Duration
	Enforcer       *sandbox.Enforcer
}

func NewDispatcher(workspaceRoot string) *Dispatcher {
	return &Dispatcher{
		WorkspaceRoot:  workspaceRoot,
		DefaultTimeout: 10 * time.Second,
	}
}

func (d *Dispatcher) Dispatch(ctx context.Context, call ToolCall) ToolResult {
	switch call.Name {
	case "shell":
		return d.handleShell(ctx, call)
	case "read_file":
		return d.handleReadFile(call)
	case "write_file":
		return d.handleWriteFile(call)
	case "list_dir":
		return d.handleListDir(call)
	default:
		return ToolResult{ToolCallID: call.ID, Output: fmt.Sprintf("unknown tool: %q", call.Name), IsError: true}
	}
}

type shellArgs struct {
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeout_ms"`
}

func (d *Dispatcher) handleShell(ctx context.Context, call ToolCall) ToolResult {
	var args shellArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return errResult(call.ID, "bad args: "+err.Error())
	}

	if d.Enforcer != nil {
		if err := d.Enforcer.CheckApproval("shell", args.Command); err != nil {
			return errResult(call.ID, err.Error())
		}
	}

	timeout := d.DefaultTimeout
	if args.TimeoutMs > 0 {
		timeout = time.Duration(args.TimeoutMs) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", args.Command)
	cmd.Dir = d.WorkspaceRoot

	out, err := cmd.CombinedOutput()
	output := string(out)

	if ctx.Err() == context.DeadlineExceeded {
		return errResult(call.ID, "command timed out after "+timeout.String()+"\n"+output)
	}
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			Output:     fmt.Sprintf("exit error: %v\n%s", err, output),
			IsError:    true,
		}
	}
	return ToolResult{ToolCallID: call.ID, Output: output}
}

type readFileArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (d *Dispatcher) handleReadFile(call ToolCall) ToolResult {
	var args readFileArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return errResult(call.ID, "bad args: "+err.Error())
	}

	abs := d.abs(args.Path)
	raw, err := os.ReadFile(abs)
	if err != nil {
		return errResult(call.ID, err.Error())
	}

	lines := strings.Split(string(raw), "\n")

	start := 1
	end := len(lines)
	if args.StartLine > 0 {
		start = args.StartLine
	}
	if args.EndLine > 0 && args.EndLine < end {
		end = args.EndLine
	}

	var sb strings.Builder
	for i := start; i <= end && i <= len(lines); i++ {
		fmt.Fprintf(&sb, "%4d\t%s\n", i, lines[i-1])
	}
	return ToolResult{ToolCallID: call.ID, Output: sb.String()}
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (d *Dispatcher) handleWriteFile(call ToolCall) ToolResult {
	var args writeFileArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return errResult(call.ID, "bad args: "+err.Error())
	}

	abs := d.abs(args.Path)

	if d.Enforcer != nil {
		if err := d.Enforcer.CheckWrite(abs); err != nil {
			return errResult(call.ID, err.Error())
		}
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return errResult(call.ID, "mkdir: "+err.Error())
	}
	if err := os.WriteFile(abs, []byte(args.Content), 0644); err != nil {
		return errResult(call.ID, err.Error())
	}
	return ToolResult{ToolCallID: call.ID, Output: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path)}
}

type listDirArgs struct {
	Path string `json:"path"`
}

func (d *Dispatcher) handleListDir(call ToolCall) ToolResult {
	var args listDirArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return errResult(call.ID, "bad args: "+err.Error())
	}
	if args.Path == "" {
		args.Path = "."
	}

	entries, err := os.ReadDir(d.abs(args.Path))
	if err != nil {
		return errResult(call.ID, err.Error())
	}

	var sb strings.Builder
	for _, e := range entries {
		kind := "file"
		if e.IsDir() {
			kind = "dir "
		}
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		fmt.Fprintf(&sb, "%s  %8d  %s\n", kind, size, e.Name())
	}
	return ToolResult{ToolCallID: call.ID, Output: sb.String()}
}

func (d *Dispatcher) abs(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(d.WorkspaceRoot, path)
}

func errResult(id, msg string) ToolResult {
	return ToolResult{ToolCallID: id, Output: msg, IsError: true}
}