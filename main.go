package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"codex-go/loop"
	"codex-go/prompt"
	"codex-go/sandbox"
	"codex-go/tools"
	"codex-go/tui"
)

func main() {
	model               := flag.String("model",                "gpt-4o-mini", "Model to use (e.g. gpt-4o, gpt-4o-mini)")
	workspace           := flag.String("workspace",            ".",           "Workspace root directory")
	maxTurns            := flag.Int("max-turns",               20,            "Max model↔tool round-trips per task")
	verbose             := flag.Bool("verbose",                false,         "Print tool calls to stderr")
	autoApprove         := flag.Bool("auto-approve",           false,         "Skip shell command approval prompts")
	task                := flag.String("task",                 "",            "Run a single task non-interactively then exit")
	maxContextTokens    := flag.Int("max-context-tokens",      128000,        "Model context window size, used to trigger compaction")
	compactionThreshold := flag.Float64("compaction-threshold", 0.75,         "Fraction of context window that triggers compaction (0–1)")
	flag.Parse()

	ws, err := resolveWorkspace(*workspace)
	if err != nil {
		fatalf("workspace error: %v", err)
	}

	policy := sandbox.DefaultPolicy(ws)
	approvalPolicy := sandbox.AskForShell
	if *autoApprove {
		approvalPolicy = sandbox.AutoApprove
	}

	enforcer := &sandbox.Enforcer{
		Policy:         policy,
		ApprovalPolicy: approvalPolicy,
	}

	dispatcher := tools.NewDispatcher(ws)
	dispatcher.Enforcer = enforcer

	pb := prompt.NewBuilder(ws)

	if *task != "" {
		enforcer.Prompter = stdinPrompter()
		agent := loop.New(loop.Config{
			Model:               *model,
			MaxTurns:            *maxTurns,
			Verbose:             *verbose,
			MaxContextTokens:    *maxContextTokens,
			CompactionThreshold: *compactionThreshold,
		}, dispatcher, pb)

		result, err := agent.Run(context.Background(), *task)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[error] %v\n", err)
			os.Exit(1)
		}
		fmt.Println(strings.Repeat("─", 60))
		fmt.Println(result)
		fmt.Println(strings.Repeat("─", 60))
		return
	}

	cfg := tui.Config{
		Model:       *model,
		Workspace:   ws,
		MaxTurns:    *maxTurns,
		Verbose:     *verbose,
		AutoApprove: *autoApprove,
		SandboxMode: policy.Mode,
	}

	app := tui.New(cfg, nil) // agent set after UIDispatcher is created

	enforcer.Prompter = func(toolName, command string) bool {
		return app.AppendApprovalRequest(toolName, command)
	}

	uiDisp := tui.NewUIDispatcher(dispatcher, app)

	agent := loop.New(loop.Config{
		Model:               *model,
		MaxTurns:            *maxTurns,
		Verbose:             *verbose,
		MaxContextTokens:    *maxContextTokens,
		CompactionThreshold: *compactionThreshold,
	}, uiDisp, pb)

	agent.OnTokenUpdate = func(total int) { app.UpdateTokens(total) }

	agent.StreamTarget = app

	app.SetAgent(agent)

	if err := app.Run(); err != nil {
		fatalf("tui error: %v", err)
	}
}

func stdinPrompter() func(string, string) bool {
	return func(toolName, command string) bool {
		fmt.Printf("\n[approval required]\ntool: %s\ncommand: %s\n\nAllow? [y/N] ", toolName, command)
		var ans string
		fmt.Scanln(&ans)
		return strings.ToLower(strings.TrimSpace(ans)) == "y"
	}
}

func resolveWorkspace(path string) (string, error) {
	if path == "." {
		return os.Getwd()
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return path, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fatal: "+format+"\n", args...)
	os.Exit(1)
}