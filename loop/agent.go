package loop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	
	"codex-go/prompt"
	"codex-go/tools"
)

type Config struct {
	APIKey   string
	Model    string
	MaxTurns int
	Verbose  bool
	MaxContextTokens    int
	CompactionThreshold float64 
}

func (c *Config) apiKey() string {
	if c.APIKey != "" {
		return c.APIKey
	}
	return os.Getenv("OPENAI_API_KEY")
}

func (c *Config) model() string {
	if c.Model != "" {
		return c.Model
	}
	return "gpt-4o-mini"
}

func (c *Config) maxTurns() int {
	if c.MaxTurns > 0 {
		return c.MaxTurns
	}
	return 20
}

func (c *Config) maxContextTokens() int {
	if c.MaxContextTokens > 0 {
		return c.MaxContextTokens
	}
	return 128_000
}

func (c *Config) compactionThreshold() float64 {
	if c.CompactionThreshold > 0 {
		return c.CompactionThreshold
	}
	return 0.75
}

type Agent struct {
	cfg        Config
	dispatcher *tools.Dispatcher
	pb         *prompt.Builder
	history    []prompt.Message
}

func New(cfg Config, dispatcher *tools.Dispatcher, pb *prompt.Builder) *Agent {
	return &Agent{cfg: cfg, dispatcher: dispatcher, pb: pb}
}

func estimateTokens(messages []prompt.Message) int {
	chars := 0
	for _, m := range messages {
		chars += contentChars(m.Content)
	}
	return chars / 4
}

func contentChars(content any) int {
	switch v := content.(type) {
	case string:
		return len(v)
	case []prompt.ContentPart:
		n := 0
		for _, p := range v {
			n += len(p.Content) + len(p.Text)
		}
		return n
	case assistantToolCallMarker:
		n := 0
		for _, c := range v.calls {
			n += len(c.Function.Name) + len(c.Function.Arguments)
		}
		return n
	default:
		return len(fmt.Sprintf("%v", v))
	}
}

type usage struct {
	totalTokens int
}

func (a *Agent) shouldCompact(estimated int, u *usage) bool {
	limit := int(float64(a.cfg.maxContextTokens()) * a.cfg.compactionThreshold())
	if u != nil {
		return u.totalTokens > limit
	}
	return estimated > limit
}

func safeCutIndex(history []prompt.Message, naive int) int {
	for i := naive; i < len(history); i++ {
		if history[i].Role != "tool" {
			if i == 0 {
				return i
			}
			prev := history[i-1]
			if prev.Role == "assistant" {
				if _, hasCalls := prev.Content.(assistantToolCallMarker); hasCalls {
					continue
				}
			}
			return i
		}
	}
	return len(history)
}

func (a *Agent) compactHistory(ctx context.Context, u *usage) (bool, error) {
	estimated := estimateTokens(a.history)
	if !a.shouldCompact(estimated, u) {
		return false, nil
	}
	if len(a.history) < 4 {
		return false, nil
	}

	naiveCut := len(a.history) / 2
	cut := safeCutIndex(a.history, naiveCut)
	if cut <= 0 || cut >= len(a.history) {
		return false, nil // no safe place to cut
	}

	toSummarize := a.history[:cut]
	kept := a.history[cut:]

	summary, err := a.summarize(ctx, toSummarize)
	if err != nil {
		if a.cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[compact] summarization failed (%v); truncating without summary\n", err)
		}
		a.history = kept
		return true, nil
	}

	summaryMsg := prompt.Message{
		Role: "user",
		Content: fmt.Sprintf(
			"<context_compaction>\nThe earlier part of this conversation was summarized to free up context space. Summary of what happened:\n\n%s\n</context_compaction>",
			summary,
		),
	}

	a.history = append([]prompt.Message{summaryMsg}, kept...)

	if a.cfg.Verbose {
		fmt.Fprintf(os.Stderr, "[compact] summarized %d messages → 1 summary message (kept %d recent messages)\n", cut, len(kept))
	}
	return true, nil
}

func (a *Agent) summarize(ctx context.Context, messages []prompt.Message) (string, error) {
	var sb strings.Builder
	for _, m := range messages {
		sb.WriteString(fmt.Sprintf("[%s] ", m.Role))
		switch v := m.Content.(type) {
		case string:
			sb.WriteString(v)
		case []prompt.ContentPart:
			for _, p := range v {
				sb.WriteString(p.Content)
				sb.WriteString(p.Text)
			}
		case assistantToolCallMarker:
			for _, c := range v.calls {
				sb.WriteString(fmt.Sprintf("called %s(%s) ", c.Function.Name, c.Function.Arguments))
			}
		}
		sb.WriteString("\n")
	}

	req := []prompt.Message{
		{
			Role: "system",
			Content: "Summarize the following coding-agent transcript in under 200 words. " +
				"Preserve: the user's original goal, decisions made, files changed and how, " +
				"commands run and their outcomes, and anything still unresolved. " +
				"Be terse and factual. Do not add commentary.",
		},
		{Role: "user", Content: sb.String()},
	}

	resp, _, err := a.callModel(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}


func (a *Agent) Run(ctx context.Context, userMessage string) (string, error) {
	a.history = append(a.history, prompt.Message{Role: "user", Content: userMessage})

	var lastUsage *usage

	for turn := 0; turn < a.cfg.maxTurns(); turn++ {
		if compacted, err := a.compactHistory(ctx, lastUsage); err != nil {
			return "", fmt.Errorf("compaction failed: %w", err)
		} else if compacted {
			lastUsage = nil 
		}

		if a.cfg.Verbose {
			fmt.Fprintf(os.Stderr, "\n[agent] turn %d\n", turn+1)
		}

		messages := []prompt.Message{
			a.pb.SystemMessage(),
			a.pb.EnvironmentContext(),
		}
		messages = append(messages, a.history...)

		resp, u, err := a.callModel(ctx, messages)
		if err != nil {
			return "", fmt.Errorf("model call failed: %w", err)
		}
		lastUsage = u

		if len(resp.ToolCalls) == 0 {
			finalMsg := resp.Content
			a.history = append(a.history, prompt.Message{Role: "assistant", Content: finalMsg})
			return finalMsg, nil
		}

		a.history = append(a.history, prompt.Message{
			Role:    "assistant",
			Content: assistantWithToolCalls(resp),
		})

		var resultParts []prompt.ContentPart
		for _, tc := range resp.ToolCalls {
			if a.cfg.Verbose {
				fmt.Fprintf(os.Stderr, "[tool] %s %s\n", tc.Name, string(tc.Args))
			}
			result := a.dispatcher.Dispatch(ctx, tc)
			if a.cfg.Verbose {
				preview := result.Output
				if len(preview) > 200 {
					preview = preview[:200] + "…"
				}
				fmt.Fprintf(os.Stderr, "[tool result] isError=%v output=%q\n", result.IsError, preview)
			}
			resultParts = append(resultParts, prompt.ContentPart{
				Type:       "tool_result",
				ToolCallID: result.ToolCallID,
				Content:    result.Output,
			})
		}
		a.history = append(a.history, prompt.Message{Role: "tool", Content: resultParts})
	}

	return "", fmt.Errorf("exceeded max turns (%d) without completing task", a.cfg.maxTurns())
}

func (a *Agent) ClearHistory() {
	a.history = nil
}

type modelResponse struct {
	Content   string
	ToolCalls []tools.ToolCall
}

type openAIRequest struct {
	Model    string       `json:"model"`
	Messages []openAIMsg  `json:"messages"`
	Tools    []openAITool `json:"tools,omitempty"`
	Stream   bool         `json:"stream"`
}

type openAIMsg struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFuncSpec `json:"function"`
}

type openAIFuncSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIUsage struct {
	TotalTokens int `json:"total_tokens"`
}

func (a *Agent) callModel(ctx context.Context, messages []prompt.Message) (modelResponse, *usage, error) {
	finalMsgs := toOpenAIMessages(messages)

	var apiTools []openAITool
	for _, spec := range tools.All() {
		paramsJSON, _ := json.Marshal(spec.Parameters)
		apiTools = append(apiTools, openAITool{
			Type: "function",
			Function: openAIFuncSpec{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  paramsJSON,
			},
		})
	}

	reqBody, err := json.Marshal(openAIRequest{
		Model:    a.cfg.model(),
		Messages: finalMsgs,
		Tools:    apiTools,
		Stream:   true,
	})
	if err != nil {
		return modelResponse{}, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.openai.com/v1/chat/completions",
		bytes.NewReader(reqBody))
	if err != nil {
		return modelResponse{}, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.apiKey())

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return modelResponse{}, nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		return modelResponse{}, nil, fmt.Errorf("API error (%d): %s", httpResp.StatusCode, string(body))
	}

	sp := NewStreamingPrinter(os.Stdout)
	resp, u, err := sp.PrintAndAccumulate(httpResp.Body)
	return resp, u, err
}

func toOpenAIMessages(messages []prompt.Message) []openAIMsg {
	var out []openAIMsg
	for _, m := range messages {
		if m.Role == "assistant" {
			if marker, ok := m.Content.(assistantToolCallMarker); ok {
				out = append(out, openAIMsg{Role: "assistant", ToolCalls: marker.calls})
				continue
			}
		}
		if m.Role == "tool" {
			if parts, ok := m.Content.([]prompt.ContentPart); ok {
				for _, p := range parts {
					out = append(out, openAIMsg{
						Role:       "tool",
						ToolCallID: p.ToolCallID,
						Content:    p.Content,
					})
				}
				continue
			}
		}
		out = append(out, openAIMsg{Role: m.Role, Content: m.Content})
	}
	return out
}

type assistantToolCallMarker struct {
	calls []openAIToolCall
}

func assistantWithToolCalls(resp modelResponse) any {
	var calls []openAIToolCall
	for _, tc := range resp.ToolCalls {
		calls = append(calls, openAIToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: tc.Name, Arguments: string(tc.Args)},
		})
	}
	return assistantToolCallMarker{calls: calls}
}

type StreamingPrinter struct {
	w io.Writer
}

func NewStreamingPrinter(w io.Writer) *StreamingPrinter {
	return &StreamingPrinter{w: w}
}

func (sp *StreamingPrinter) PrintAndAccumulate(r io.Reader) (modelResponse, *usage, error) {
	var finalResp modelResponse
	var u *usage
	toolCallMap := make(map[int]*tools.ToolCall)

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *openAIUsage `json:"usage"`
		}

		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			u = &usage{totalTokens: chunk.Usage.TotalTokens}
		}
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				fmt.Fprint(sp.w, delta.Content)
				finalResp.Content += delta.Content
			}
			for _, tcDelta := range delta.ToolCalls {
				tc, exists := toolCallMap[tcDelta.Index]
				if !exists {
					tc = &tools.ToolCall{ID: tcDelta.ID, Name: tcDelta.Function.Name}
					toolCallMap[tcDelta.Index] = tc
				}
				tc.Args = append(tc.Args, []byte(tcDelta.Function.Arguments)...)
			}
		}
	}

	for _, tc := range toolCallMap {
		finalResp.ToolCalls = append(finalResp.ToolCalls, *tc)
	}

	fmt.Fprintln(sp.w)
	return finalResp, u, scanner.Err()
}