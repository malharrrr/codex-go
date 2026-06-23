package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"codex-go/loop"
	"codex-go/sandbox"
)

type Config struct {
	Model       string
	Workspace   string
	MaxTurns    int
	Verbose     bool
	AutoApprove bool
	SandboxMode sandbox.Mode
}

type App struct {
	cfg   Config
	agent *loop.Agent

	tapp      *tview.Application
	conv      *tview.TextView
	input     *tview.InputField
	statusBar *tview.TextView

	mu        sync.Mutex
	turns     int
	toolCalls int
	tokens    int
	running   bool
	streamWriter *tuiWriter
}

type tuiWriter struct {
	app *App
	buf strings.Builder
}

func (w *tuiWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		s := w.buf.String()
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			break
		}
		line := s[:idx]
		w.buf.Reset()
		w.buf.WriteString(s[idx+1:])
		w.app.tapp.QueueUpdateDraw(func() {
			w.app.appendLine(labelAgent() + bodyText(tview.Escape(line)))
		})
	}
	return len(p), nil
}

func (w *tuiWriter) flush() {
	if s := w.buf.String(); s != "" {
		w.buf.Reset()
		w.app.tapp.QueueUpdateDraw(func() {
			w.app.appendLine(labelAgent() + bodyText(tview.Escape(s)))
		})
	}
}

func (a *App) Write(p []byte) (int, error) {
	return a.streamWriter.Write(p)
}

func (a *App) UpdateTokens(total int) {
	a.tapp.QueueUpdateDraw(func() {
		a.mu.Lock()
		a.tokens = total
		a.mu.Unlock()
		a.refreshStatus()
	})
}

func New(cfg Config, agent *loop.Agent) *App {
	return &App{cfg: cfg, agent: agent}
}

func (a *App) SetAgent(agent *loop.Agent) {
	a.agent = agent
}

func (a *App) Run() error {
	a.tapp = tview.NewApplication()
	a.streamWriter = &tuiWriter{app: a}

	a.conv = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWordWrap(true).
		SetChangedFunc(func() {
			a.tapp.Draw()
			a.conv.ScrollToEnd()
		})
	a.conv.SetBorder(false).
		SetBackgroundColor(tcell.ColorDefault)

	a.statusBar = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	a.statusBar.SetBorder(false).
		SetBackgroundColor(tcell.NewRGBColor(0x16, 0x16, 0x18))

	a.input = tview.NewInputField().
		SetLabel(accentText(" › ")).
		SetFieldBackgroundColor(tcell.ColorDefault).
		SetLabelColor(tcell.ColorDefault).
		SetFieldTextColor(tcell.NewRGBColor(0xe2, 0xe8, 0xf0)).
		SetPlaceholder("describe a coding task…  (/help for commands)").
		SetPlaceholderStyle(tcell.StyleDefault.Foreground(tcell.NewRGBColor(0x37, 0x41, 0x51)))

	a.input.SetBorder(true).
		SetBorderColor(tcell.NewRGBColor(0x2a, 0x2a, 0x2e)).
		SetBackgroundColor(tcell.ColorDefault).
		SetTitle("  [Enter] send   [Esc] clear   [PgUp/PgDn] scroll   [Ctrl+C] quit  ").
		SetTitleColor(tcell.NewRGBColor(0x37, 0x41, 0x51)).
		SetTitleAlign(tview.AlignRight)

	a.input.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEnter:
			a.submit()
		case tcell.KeyEscape:
			a.input.SetText("")
		}
	})

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.statusBar, 1, 0, false).
		AddItem(a.conv, 0, 1, false).
		AddItem(a.input, 3, 0, true)

	a.tapp.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyCtrlC:
			a.tapp.Stop()
			return nil
		case tcell.KeyPgUp:
			row, _ := a.conv.GetScrollOffset()
			a.conv.ScrollTo(row-10, 0)
			return nil
		case tcell.KeyPgDn:
			row, _ := a.conv.GetScrollOffset()
			a.conv.ScrollTo(row+10, 0)
			return nil
		}
		return event
	})

	a.refreshStatus()
	a.appendLine("")
	a.appendLine(accentText("  ⬡ codex-go") + dimText("  —  a minimal agentic coding harness"))
	a.appendLine(dimText(fmt.Sprintf("  model=%s   workspace=%s   sandbox=%s",
		a.cfg.Model, a.cfg.Workspace,
		sandboxLabel(a.cfg.SandboxMode, a.cfg.AutoApprove))))
	a.appendLine(dimText("  type a task and press Enter  ·  /help for commands"))
	a.appendLine("")

	return a.tapp.SetRoot(flex, true).EnableMouse(false).Run()
}

func (a *App) submit() {
	text := strings.TrimSpace(a.input.GetText())
	if text == "" {
		return
	}
	a.input.SetText("")

	if strings.HasPrefix(text, "/") {
		a.handleCommand(text)
		return
	}

	a.mu.Lock()
	busy := a.running
	a.mu.Unlock()
	if busy {
		a.appendLine(warnText("  ⚠ agent is still running — wait for it to finish"))
		return
	}

	a.appendLine("")
	a.appendLine(labelUser() + bodyText(text))
	a.appendLine("")

	a.mu.Lock()
	a.running = true
	a.mu.Unlock()
	a.refreshStatus()
	a.input.SetLabel(warnText(" ● "))

	go func() {
		_, err := a.agent.Run(context.Background(), text)
		a.streamWriter.flush()

		a.tapp.QueueUpdateDraw(func() {
			a.mu.Lock()
			a.turns++
			a.running = false
			a.mu.Unlock()

			if err != nil {
				a.appendLine(errText("  error: " + tview.Escape(err.Error())))
			}
			a.appendLine("")
			a.refreshStatus()
			a.input.SetLabel(accentText(" › "))
		})
	}()
}

func (a *App) handleCommand(cmd string) {
	parts := strings.Fields(cmd)
	switch parts[0] {
	case "/clear":
		a.agent.ClearHistory()
		a.conv.Clear()
		a.mu.Lock()
		a.turns = 0
		a.toolCalls = 0
		a.mu.Unlock()
		a.refreshStatus()
		a.appendLine(dimText("  · history cleared"))
		a.appendLine("")

	case "/help":
		a.appendLine("")
		a.appendLine(accentText("  commands"))
		a.appendLine(dimText("  /clear     ") + "reset conversation history")
		a.appendLine(dimText("  /status    ") + "show session info")
		a.appendLine(dimText("  /quit      ") + "exit")
		a.appendLine(dimText("  /help      ") + "show this")
		a.appendLine("")
		a.appendLine(accentText("  keys"))
		a.appendLine(dimText("  PgUp/PgDn  ") + "scroll conversation")
		a.appendLine(dimText("  Esc        ") + "clear input field")
		a.appendLine(dimText("  Ctrl+C     ") + "quit")
		a.appendLine("")

	case "/status":
		a.mu.Lock()
		turns := a.turns
		toolCalls := a.toolCalls
		a.mu.Unlock()
		a.appendLine("")
		a.appendLine(accentText("  session"))
		a.appendLine(dimText("  model      ") + a.cfg.Model)
		a.appendLine(dimText("  workspace  ") + a.cfg.Workspace)
		a.appendLine(dimText("  sandbox    ") + sandboxLabel(a.cfg.SandboxMode, a.cfg.AutoApprove))
		a.appendLine(dimText("  turns      ") + fmt.Sprintf("%d", turns))
		a.appendLine(dimText("  tool calls ") + fmt.Sprintf("%d", toolCalls))
		a.appendLine("")

	case "/quit", "/exit":
		a.tapp.Stop()

	default:
		a.appendLine(errText(fmt.Sprintf("  unknown command: %s  (try /help)", parts[0])))
	}
}

func (a *App) AppendToolCall(name, preview string) {
	a.tapp.QueueUpdateDraw(func() {
		a.mu.Lock()
		a.toolCalls++
		a.mu.Unlock()
		a.refreshStatus()
		if len(preview) > 120 {
			preview = preview[:120] + "…"
		}
		a.appendLine(labelTool(name) + dimText(tview.Escape(preview)))
	})
}

func (a *App) AppendToolResult(name, output string, isError bool) {
	a.tapp.QueueUpdateDraw(func() {
		lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
		shown := lines
		extra := 0
		if len(lines) > 6 {
			shown = lines[:6]
			extra = len(lines) - 6
		}
		label := labelOK(name)
		if isError {
			label = labelErr(name)
		}
		indent := dimText("              ")
		for i, l := range shown {
			if i == 0 {
				a.appendLine(label + dimText(tview.Escape(l)))
			} else {
				a.appendLine(indent + dimText(tview.Escape(l)))
			}
		}
		if extra > 0 {
			a.appendLine(indent + dimText(fmt.Sprintf("… +%d lines", extra)))
		}
	})
}

func (a *App) AppendApprovalRequest(toolName, command string) bool {
	resultCh := make(chan bool, 1)

	a.tapp.QueueUpdateDraw(func() {
		a.appendLine("")
		a.appendLine(labelApproval() + warnText("shell command requires approval"))
		a.appendLine(dimText("              $ " + tview.Escape(command)))
		a.appendLine(dimText("              ") +
			warnText("[y]") + dimText(" allow   ") +
			errText("[n]") + dimText(" deny"))
		a.appendLine("")
		a.input.SetLabel(warnText(" y/n › "))

		prev := a.tapp.GetInputCapture()
		a.tapp.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			r := event.Rune()
			if r == 'y' || r == 'Y' {
				a.tapp.SetInputCapture(prev)
				a.input.SetLabel(accentText(" › "))
				a.appendLine(labelOK("approved") + dimText("$ "+tview.Escape(command)))
				a.appendLine("")
				resultCh <- true
				return nil
			}
			if r == 'n' || r == 'N' || event.Key() == tcell.KeyEnter {
				a.tapp.SetInputCapture(prev)
				a.input.SetLabel(accentText(" › "))
				a.appendLine(labelDenied() + errText("denied"))
				a.appendLine("")
				resultCh <- false
				return nil
			}
			return nil
		})
	})

	return <-resultCh
}


func (a *App) appendLine(s string) {
	fmt.Fprintln(a.conv, s)
}

func (a *App) refreshStatus() {
	a.mu.Lock()
	turns := a.turns
	toolCalls := a.toolCalls
	tokens := a.tokens
	running := a.running
	a.mu.Unlock()

	state := dimText("idle")
	if running {
		state = warnText("running")
	}

	tokStr := dimText("—")
	if tokens > 0 {
		tokStr = accentText(fmt.Sprintf("%d", tokens))
	}

	bar := fmt.Sprintf("  %s   %s   %s   %s   turns:%s  tools:%s  tokens:%s  %s",
		accentText("⬡ codex-go"),
		dimText("model:")+a.cfg.Model,
		dimText("ws:")+a.cfg.Workspace,
		dimText("sandbox:")+sandboxLabel(a.cfg.SandboxMode, a.cfg.AutoApprove),
		accentText(fmt.Sprintf("%d", turns)),
		accentText(fmt.Sprintf("%d", toolCalls)),
		tokStr,
		state,
	)
	a.statusBar.SetText(bar)
}

func sandboxLabel(mode sandbox.Mode, autoApprove bool) string {
	if autoApprove {
		return warnText("auto-approve")
	}
	switch mode {
	case sandbox.FullAccess:
		return errText("full-access")
	case sandbox.ReadOnly:
		return okText("read-only")
	default:
		return okText("workspace-write")
	}
}