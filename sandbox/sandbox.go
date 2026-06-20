package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Policy struct {
	Mode          Mode
	WritableRoots []string
	ReadonlyPaths []string
}

type Mode int

const (
	FullAccess Mode = iota
	WorkspaceWrite
	ReadOnly
)

func (m Mode) String() string {
	switch m {
	case FullAccess:
		return "full-access"
	case WorkspaceWrite:
		return "workspace-write"
	case ReadOnly:
		return "read-only"
	}
	return "unknown"
}

func DefaultPolicy(workspaceRoot string) Policy {
	return Policy{
		Mode:          WorkspaceWrite,
		WritableRoots: []string{workspaceRoot},
		ReadonlyPaths: []string{
			filepath.Join(workspaceRoot, ".git"),
			filepath.Join(workspaceRoot, ".codex"),
		},
	}
}

type ApprovalPolicy int

const (
	AutoApprove ApprovalPolicy = iota
	AskForShell
	AlwaysAsk
)

type Enforcer struct {
	Policy         Policy
	ApprovalPolicy ApprovalPolicy
	Prompter       func(toolName, command string) bool
}

func isWithin(candidate, root string) bool {
	c := filepath.Clean(candidate)
	r := filepath.Clean(root)
	if c == r {
		return true
	}
	return strings.HasPrefix(c, r+string(filepath.Separator))
}

func (e *Enforcer) CheckWrite(path string) error {
	if e.Policy.Mode == FullAccess {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("cannot resolve path %q: %w", path, err)
	}

	for _, ro := range e.Policy.ReadonlyPaths {
		if isWithin(abs, ro) {
			return fmt.Errorf("write blocked: %q is in a read-only path (%s)", path, ro)
		}
	}

	if e.Policy.Mode == ReadOnly {
		return fmt.Errorf("write blocked: sandbox is read-only")
	}

	for _, root := range e.Policy.WritableRoots {
		if isWithin(abs, root) {
			return nil
		}
	}
	return fmt.Errorf("write blocked: %q is outside writable roots %v", path, e.Policy.WritableRoots)
}

func (e *Enforcer) CheckApproval(toolName, command string) error {
	switch e.ApprovalPolicy {
	case AutoApprove:
		return nil
	case AskForShell:
		if toolName != "shell" {
			return nil
		}
	}
	if e.Prompter != nil && !e.Prompter(toolName, command) {
		return fmt.Errorf("tool call denied by user")
	}
	return nil
}