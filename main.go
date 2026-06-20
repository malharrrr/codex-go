package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"codex-go/loop"
	"codex-go/prompt"
	"codex-go/sandbox"
	"codex-go/tools"
)

func main() {
	model := flag.String("model", "gpt-4o-mini", "Model to use (e.g. gpt-4o, gpt-4o-mini)")
	workspace := flag.String("workspace", ".", "Workspace root directory (agent can read/write here)")
	maxTurns := flag.Int("max-turns", 20, "Max model↔tool round-trips per task")
	verbose := flag.Bool("verbose", false, "Print tool calls and results to stderr")
	autoApprove := flag.Bool("auto-approve", false, "Skip confirmation prompts for shell commands")
	task := flag.String("task", "", "Run a single task non-interactively then exit")
	maxContextTokens := flag.Int("max-context-tokens", 128000, "Model context window size, used to trigger compaction")
	compactionThreshold := flag.Float64("compaction-threshold", 0.75, "Fraction of context window that triggers compaction (0-1)")
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
		Prompter: func(toolName, command string) bool {
			fmt.Printf("\n[approval required]\ntool: %s\ncommand: %s\n\nAllow? [y/N] ", toolName, command)
			var ans string
			fmt.Scanln(&ans)
			return strings.ToLower(strings.TrimSpace(ans)) == "y"
		},
	}

	dispatcher := tools.NewDispatcher(ws)
	dispatcher.Enforcer = enforcer

	pb := prompt.NewBuilder(ws)

	agent := loop.New(loop.Config{
		Model:               *model,
		MaxTurns:            *maxTurns,
		Verbose:             *verbose,
		MaxContextTokens:    *maxContextTokens,
		CompactionThreshold: *compactionThreshold,
	}, dispatcher, pb)

	if *task != "" {
		runTask(agent, *task)
		return
	}

	fmt.Printf("codex-go  model=%s  workspace=%s  sandbox=%s\n",
		*model, ws, policy.Mode)
	fmt.Println("Type your coding task. Commands: /clear  /exit")
	fmt.Println(strings.Repeat("─", 60))

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		switch input {
		case "/exit", "/quit":
			fmt.Println("bye")
			return
		case "/clear":
			agent.ClearHistory()
			fmt.Println("[history cleared]")
			continue
		case "/help":
			printHelp()
			continue
		}

		runTask(agent, input)
	}
}

func runTask(agent *loop.Agent, task string) {
	fmt.Println()
	result, err := agent.Run(context.Background(), task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[error] %v\n", err)
		return
	}
	fmt.Println(strings.Repeat("─", 60))
	fmt.Print(result)
	fmt.Println()
	fmt.Println(strings.Repeat("─", 60))
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

func printHelp() {
	fmt.Print(`
Commands:
  /clear    Clear conversation history (start fresh)
  /exit     Exit the REPL
  /help     Show this help

Flags (set at startup):
  -model         Model name (default: gpt-4o-mini)
  -workspace     Directory the agent can read/write (default: .)
  -max-turns     Max tool round-trips per task (default: 20)
  -verbose       Print tool calls to stderr
  -auto-approve  Skip shell command approval prompts
  -task          Run one task non-interactively then exit
  -max-context-tokens     Model context window (default: 128000)
  -compaction-threshold   Fraction of window that triggers summarization (default: 0.75)

Example tasks:
  > List all Go files in this project and count lines of code
  > Write a function to reverse a string and add tests for it
  > Find all TODO comments in the codebase
  > Refactor main.go to extract the REPL into its own file
`)
}