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
	"sync"

	"codex-go/prompt"
	"codex-go/tools"
)

type Config struct {
	APIKey   string
	Model    string
	MaxTurns int
	Verbose  bool
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

type Agent struct {
	cfg        Config
	dispatcher *tools.Dispatcher
	pb         *prompt.Builder
	history    []prompt.Message 
}

func New(cfg Config, dispatcher *tools.Dispatcher, pb *prompt.Builder) *Agent {
	return &Agent{
		cfg:        cfg,
		dispatcher: dispatcher,
		pb:         pb,
	}
}

func estimateTokens(messages []prompt.Message) int {
	chars := 0
	for _, m := range messages {
		if str, ok := m.Content.(string); ok {
			chars += len(str)
		} else if parts, ok := m.Content.([]prompt.ContentPart); ok {
			for _, p := range parts {
				chars += len(p.Content)
			}
		}
	}
	return chars / 4
}

func (a *Agent) compactHistory() {
	const maxTokens = 6000

	if estimateTokens(a.history) > maxTokens {
		cutoff := int(float64(len(a.history)) * 0.4)

		if cutoff%2 != 0 {
			cutoff++
		}

		a.history = a.history[cutoff:]
		if a.cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[system] compacted history to prevent token overflow\n")
		}
	}
}

func (a *Agent) Run(ctx context.Context, userMessage string) (string, error) {
	a.history = append(a.history, prompt.Message{
		Role:    "user",
		Content: userMessage,
	})

	for turn := 0; turn < a.cfg.maxTurns(); turn++ {
		a.compactHistory()

		if a.cfg.Verbose {
			fmt.Fprintf(os.Stderr, "\n[agent] turn %d\n", turn+1)
		}
		messages := []prompt.Message{
			a.pb.SystemMessage(),
			a.pb.EnvironmentContext(),
		}
		messages = append(messages, a.history...)

		resp, err := a.callModel(ctx, messages)
		if err != nil {
			return "", fmt.Errorf("model call failed: %w", err)
		}

		if len(resp.ToolCalls) == 0 {
			finalMsg := resp.Content
			a.history = append(a.history, prompt.Message{
				Role:    "assistant",
				Content: finalMsg,
			})
			return finalMsg, nil
		}

		a.history = append(a.history, prompt.Message{
			Role:    "assistant",
			Content: assistantWithToolCalls(resp),
		})

		var resultParts []prompt.ContentPart
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, tc := range resp.ToolCalls {
			wg.Add(1)
			go func(call tools.ToolCall) {
				defer wg.Done()

				if a.cfg.Verbose {
					mu.Lock()
					fmt.Fprintf(os.Stderr, "[tool] %s %s\n", call.Name, string(call.Args))
					mu.Unlock()
				}
				
				result := a.dispatcher.Dispatch(ctx, call)
				
				if a.cfg.Verbose {
					preview := result.Output
					if len(preview) > 200 {
						preview = preview[:200] + "…"
					}
					mu.Lock()
					fmt.Fprintf(os.Stderr, "[tool result] isError=%v output=%q\n", result.IsError, preview)
					mu.Unlock()
				}

				mu.Lock()
				resultParts = append(resultParts, prompt.ContentPart{
					Type:       "tool_result",
					ToolCallID: result.ToolCallID,
					Content:    result.Output,
				})
				mu.Unlock()
			}(tc)
		}
		wg.Wait()

		a.history = append(a.history, prompt.Message{
			Role:    "tool",
			Content: resultParts,
		})
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
	Model    string         `json:"model"`
	Messages []openAIMsg    `json:"messages"`
	Tools    []openAITool   `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
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

func (a *Agent) callModel(ctx context.Context, messages []prompt.Message) (modelResponse, error) {
	var apiMsgs []openAIMsg
	for _, m := range messages {
		apiMsg := openAIMsg{Role: m.Role}
		switch v := m.Content.(type) {
		case string:
			apiMsg.Content = v
		case []prompt.ContentPart:
			apiMsg.Content = nil
			apiMsg.ToolCallID = v[0].ToolCallID
			apiMsg.Content = v[0].Content
		default:
			apiMsg.Content = fmt.Sprintf("%v", v)
		}
		apiMsgs = append(apiMsgs, apiMsg)
	}

	var expandedMsgs []openAIMsg
	for _, m := range apiMsgs {
		if m.Role == "tool" {
			expandedMsgs = append(expandedMsgs, m)
			continue
		}
		expandedMsgs = append(expandedMsgs, m)
	}

	var finalMsgs []openAIMsg
	for _, m := range expandedMsgs {
		if m.Role == "assistant" {
			if marker, ok := m.Content.(assistantToolCallMarker); ok {
				finalMsgs = append(finalMsgs, openAIMsg{
					Role:      "assistant",
					ToolCalls: marker.calls,
				})
				continue
			}
		}
		finalMsgs = append(finalMsgs, m)
	}

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
		return modelResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.openai.com/v1/chat/completions",
		bytes.NewReader(reqBody))
	if err != nil {
		return modelResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.apiKey())

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return modelResponse{}, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		return modelResponse{}, fmt.Errorf("API error (%d): %s", httpResp.StatusCode, string(body))
	}

	sp := NewStreamingPrinter(os.Stdout)
	return sp.PrintAndAccumulate(httpResp.Body)
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
			}{
				Name:      tc.Name,
				Arguments: string(tc.Args),
			},
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

func (sp *StreamingPrinter) PrintAndAccumulate(r io.Reader) (modelResponse, error) {
	var finalResp modelResponse
	var toolCallMap = make(map[int]*tools.ToolCall)
	
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
		}
		
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			continue
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
	return finalResp, scanner.Err()
}