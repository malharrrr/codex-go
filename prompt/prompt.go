package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Message struct {
	Role    string `json:"role"` 
	Content any    `json:"content"` 
}

type ContentPart struct {
	Type       string `json:"type"`               
	Text       string `json:"text,omitempty"`       
	ToolCallID string `json:"tool_call_id,omitempty"` 
	Content    string `json:"content,omitempty"`     
}

type Builder struct {
	WorkspaceRoot string
	BaseInstructions string
}

func NewBuilder(workspaceRoot string) *Builder {
	return &Builder{
		WorkspaceRoot:    workspaceRoot,
		BaseInstructions: defaultBaseInstructions,
	}
}

func (b *Builder) SystemMessage() Message {
	parts := []string{b.BaseInstructions}

	if agentsDoc := b.loadAgentsMD(); agentsDoc != "" {
		parts = append(parts, "\n\n<agents_md>\n"+agentsDoc+"\n</agents_md>")
	}

	return Message{Role: "system", Content: strings.Join(parts, "")}
}

func (b *Builder) EnvironmentContext() Message {
	cwd, _ := os.Getwd()
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	content := fmt.Sprintf(
		"<environment>\ncwd: %s\nshell: %s\nos: %s/%s\n</environment>",
		cwd, shell, runtime.GOOS, runtime.GOARCH,
	)
	return Message{Role: "user", Content: content}
}

func (b *Builder) loadAgentsMD() string {
	candidates := []string{
		filepath.Join(b.WorkspaceRoot, "AGENTS.md"),
		filepath.Join(b.WorkspaceRoot, ".codex", "instructions.md"),
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}
	}
	return ""
}

const defaultBaseInstructions = `You are a coding agent running in a terminal. You have access to tools to read and write files, run shell commands, and list directories.

Your goal is to complete the user's coding task completely and correctly.

Guidelines:
- Think step-by-step before acting. Plan what you need to do, then execute.
- Prefer read_file over shell cat for reading source files.
- After writing or editing code, run the relevant tests or linter to verify.
- If a command fails, read the error carefully and try to fix the root cause.
- When finished, summarize what you did and what changed.
- Never ask the user clarifying questions mid-task unless truly blocked. Make a reasonable assumption and proceed.`
