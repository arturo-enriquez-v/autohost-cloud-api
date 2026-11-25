// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Message is a chat message in the OpenAI chat completions format, which Ollama, llama.cpp, vLLM, LM
// Studio and OpenAI itself accept.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a call the model asks for.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the tool name and its JSON arguments (as a string, as in the OpenAI format).
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toolSpec is a tool definition sent to the model.
type toolSpec struct {
	Type     string       `json:"type"`
	Function functionSpec `json:"function"`
}

type functionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// completionRequest is the body of POST {base}/chat/completions.
type completionRequest struct {
	Model           string     `json:"model"`
	Messages        []Message  `json:"messages"`
	Tools           []toolSpec `json:"tools,omitempty"`
	Stream          bool       `json:"stream"`
	ReasoningEffort string     `json:"reasoning_effort,omitempty"`
}

// streamChunk is one server-sent event of a streamed completion.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// streamHandlers receive the parts of a completion as they arrive.
type streamHandlers struct {
	onContent   func(string)
	onReasoning func(string)
}

// ErrProvider wraps failures talking to the model provider.
var ErrProvider = errors.New("model provider error")

// Provider talks to an OpenAI-compatible chat completions endpoint.
type Provider struct {
	BaseURL string // e.g. http://localhost:11434/v1
	APIKey  string
	Client  *http.Client
}

func (p *Provider) endpoint(path string) string {
	return strings.TrimRight(p.BaseURL, "/") + path
}

func (p *Provider) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.endpoint(path), reader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		var parsed struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		text := strings.TrimSpace(string(msg))
		if json.Unmarshal(msg, &parsed) == nil && parsed.Error.Message != "" {
			text = parsed.Error.Message
		}
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrProvider, resp.StatusCode, text)
	}
	return resp, nil
}

// Models lists the models the provider serves (GET {base}/models).
func (p *Provider) Models(ctx context.Context) ([]string, error) {
	resp, err := p.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: invalid models response", ErrProvider)
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out, nil
}

// Stream runs one streamed completion and returns the assistant message it produced (text and tool
// calls). Text and reasoning are passed to the handlers as they arrive.
func (p *Provider) Stream(ctx context.Context, req completionRequest, h streamHandlers) (Message, error) {
	req.Stream = true
	resp, err := p.do(ctx, http.MethodPost, "/chat/completions", req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()

	var content strings.Builder
	calls := map[int]*ToolCall{}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return Message{}, fmt.Errorf("%w: %s", ErrProvider, chunk.Error.Message)
		}
		for _, choice := range chunk.Choices {
			d := choice.Delta
			if r := d.Reasoning + d.ReasoningContent; r != "" && h.onReasoning != nil {
				h.onReasoning(r)
			}
			if d.Content != "" {
				content.WriteString(d.Content)
				if h.onContent != nil {
					h.onContent(d.Content)
				}
			}
			// Tool calls may arrive in pieces (OpenAI) or whole (Ollama): accumulate by index.
			for _, tc := range d.ToolCalls {
				c, ok := calls[tc.Index]
				if !ok {
					c = &ToolCall{Type: "function"}
					calls[tc.Index] = c
				}
				if tc.ID != "" {
					c.ID = tc.ID
				}
				if tc.Function.Name != "" {
					c.Function.Name += tc.Function.Name
				}
				c.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrProvider, err)
	}

	msg := Message{Role: "assistant", Content: content.String()}
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for n, i := range idx {
		c := calls[i]
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), n)
		}
		if strings.TrimSpace(c.Function.Arguments) == "" {
			c.Function.Arguments = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, *c)
	}
	return msg, nil
}
