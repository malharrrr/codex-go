package tui

import "fmt"

const (
	colorAccent  = "#a78bfa" // purple  — logo, agent label
	colorUser    = "#60a5fa" // blue    — user label
	colorTool    = "#f59e0b" // amber   — tool-call line
	colorOK      = "#4ade80" // green   — successful tool result
	colorErr     = "#f87171" // red     — error result / denied
	colorMuted   = "#6b7280" // gray    — tool output body, hints
	colorWarning = "#fbbf24" // yellow  — approval prompt
	colorText    = "#e2e8f0" // near-white — agent body text
)

func tag(c, s string) string { return fmt.Sprintf("[%s]%s[-]", c, s) }

func labelUser() string             { return tag(colorUser, "[you]   ") }
func labelAgent() string            { return tag(colorAccent, "[agent] ") }
func labelTool(name string) string  { return tag(colorTool, fmt.Sprintf("  ▶ %-10s  ", name)) }
func labelOK(name string) string    { return tag(colorOK, fmt.Sprintf("  ✓ %-10s  ", name)) }
func labelErr(name string) string   { return tag(colorErr, fmt.Sprintf("  ✗ %-10s  ", name)) }
func labelApproval() string         { return tag(colorWarning, "  ⚠ approval  ") }
func labelDenied() string           { return tag(colorErr, "  ✗ denied    ") }

func dimText(s string) string    { return tag(colorMuted, s) }
func accentText(s string) string { return tag(colorAccent, s) }
func errText(s string) string    { return tag(colorErr, s) }
func okText(s string) string     { return tag(colorOK, s) }
func warnText(s string) string   { return tag(colorWarning, s) }
func bodyText(s string) string   { return tag(colorText, s) }