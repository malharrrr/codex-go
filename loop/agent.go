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
	APIKey string
	Model string
	MaxTurns int
	Verbose bool
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
	history    []prompt.Message // full conversation history, excluding system/env
}

func New(cfg Config, dispatcher *tools.Dispatcher, pb *prompt.Builder) *Agent {
	return &Agent{
		cfg:        cfg,
		dispatcher: dispatcher,
		pb:         pb,
	}
}

func (a *Agent) Run(ctx context.Context, userMessage string) (string, error) {
	a.history = append(a.history, prompt.Message{
		Role:    "user",
		Content: userMessage,
	})

	for turn := 0; turn < a.cfg.maxTurns(); turn++ {
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
	Role       string              `json:"role"`
	Content    any                 `json:"content,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall    `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string          `json:"type"`
	Function openAIFuncSpec  `json:"function"`
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

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
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
		Stream:   false,
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

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return modelResponse{}, err
	}

	var apiResp openAIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return modelResponse{}, fmt.Errorf("bad response JSON: %w\nbody: %s", err, body)
	}
	if apiResp.Error != nil {
		return modelResponse{}, fmt.Errorf("API error: %s", apiResp.Error.Message)
	}
	if len(apiResp.Choices) == 0 {
		return modelResponse{}, fmt.Errorf("no choices in response")
	}

	choice := apiResp.Choices[0].Message

	var tcs []tools.ToolCall
	for _, tc := range choice.ToolCalls {
		tcs = append(tcs, tools.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: json.RawMessage(tc.Function.Arguments),
		})
	}

	return modelResponse{
		Content:   choice.Content,
		ToolCalls: tcs,
	}, nil
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

func (sp *StreamingPrinter) Print(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			fmt.Fprint(sp.w, chunk.Choices[0].Delta.Content)
		}
	}
}
