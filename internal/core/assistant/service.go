// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

// Package assistant is the panel's built-in chat: a tool-calling loop between an OpenAI-compatible model
// (Ollama, llama.cpp, vLLM, OpenAI...) and the AutoHost API. Tools run through the API router with the
// user's own credentials, so the assistant can do exactly what the user can. Calls that change something
// never run on the model's word: the loop stops and resumes only when the user approves them.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/api"
	"github.com/arturo/autohost-cloud-api/internal/mcpserver"
	"github.com/arturo/autohost-cloud-api/internal/platform/secretbox"
)

const (
	// maxSteps bounds model calls per request (each may call tools).
	maxSteps = 8
	// maxMessages and maxHistoryBytes bound the conversation a client sends.
	maxMessages     = 200
	maxHistoryBytes = 1 << 20
	// contextMessages is how many recent messages are sent to the model.
	contextMessages = 40
	// Tool results older than recentToolMessages messages and longer than oldToolResultChars are
	// replaced by a note. They are dropped whole, not cut: a list cut in half made a model pair IDs with
	// the wrong names.
	recentToolMessages = 16
	oldToolResultChars = 1500
)

// Event is a server-sent event of a chat turn.
type Event struct {
	Type string
	Data any
}

// ChatRequest is one turn: the conversation so far (kept by the client) and, to resume a turn that
// stopped for approval, the user's decision per tool call ID.
type ChatRequest struct {
	Messages  []Message       `json:"messages"`
	Approvals map[string]bool `json:"approvals,omitempty"`
	Model     string          `json:"model,omitempty"`
	ReadOnly  bool            `json:"read_only,omitempty"`
}

// Service runs the assistant.
type Service struct {
	repo   SettingsRepository
	box    *secretbox.Box
	index  *mcpserver.Index
	tools  []*Tool
	client *http.Client
	now    func() time.Time
}

// NewService builds the service; the operations index comes from the embedded OpenAPI spec.
func NewService(repo SettingsRepository) (*Service, error) {
	box, err := secretbox.FromEnv()
	if err != nil {
		return nil, err
	}
	idx, err := mcpserver.LoadIndex(api.OpenAPISpec)
	if err != nil {
		return nil, err
	}
	return NewServiceWith(repo, box, idx, &http.Client{Timeout: 10 * time.Minute}), nil
}

// NewServiceWith builds the service with explicit dependencies (tests).
func NewServiceWith(repo SettingsRepository, box *secretbox.Box, idx *mcpserver.Index, client *http.Client) *Service {
	return &Service{repo: repo, box: box, index: idx, tools: tools(), client: client, now: time.Now}
}

// Index returns the API operations index the tools use.
func (s *Service) Index() *mcpserver.Index { return s.index }

// Settings returns the saved settings, or the environment's when nothing was saved.
func (s *Service) Settings(ctx context.Context) (*Settings, error) {
	saved, err := s.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return envSettings(), nil
	}
	saved.HasAPIKey = saved.APIKeyCiphertext != ""
	if saved.Models == nil {
		saved.Models = []string{}
	}
	return saved, nil
}

// SaveSettings validates and stores the settings (admins).
func (s *Service) SaveSettings(ctx context.Context, in SettingsInput, updatedBy string) (*Settings, error) {
	if err := validateSettings(&in); err != nil {
		return nil, err
	}
	current, err := s.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	next := &Settings{Enabled: in.Enabled, BaseURL: in.BaseURL, DefaultModel: in.DefaultModel, Models: in.Models, ReasoningEffort: in.ReasoningEffort}
	switch {
	case in.APIKey == nil && current != nil:
		next.APIKeyCiphertext = current.APIKeyCiphertext
	case in.APIKey != nil && *in.APIKey != "":
		if !s.box.Configured() {
			return nil, fmt.Errorf("%w: SECRETS_ENCRYPTION_KEY is not set, the API key cannot be stored", ErrInvalid)
		}
		sealed, err := s.box.Seal(*in.APIKey, settingsAAD)
		if err != nil {
			return nil, err
		}
		next.APIKeyCiphertext = sealed
	}
	saved, err := s.repo.Save(ctx, next, updatedBy)
	if err != nil {
		return nil, err
	}
	saved.HasAPIKey = saved.APIKeyCiphertext != ""
	return saved, nil
}

// provider returns the client for the configured model provider.
func (s *Service) provider(st *Settings) (*Provider, error) {
	key := ""
	switch {
	case st.FromEnv:
		key = os.Getenv("ASSISTANT_API_KEY")
	case st.APIKeyCiphertext != "":
		k, err := s.box.Open(st.APIKeyCiphertext, settingsAAD)
		if err != nil {
			return nil, err
		}
		key = k
	}
	return &Provider{BaseURL: st.BaseURL, APIKey: key, Client: s.client}, nil
}

// ProviderModels lists the models the configured provider serves (to pick them in the settings).
func (s *Service) ProviderModels(ctx context.Context) ([]string, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if st.BaseURL == "" {
		return nil, ErrNotConfigured
	}
	p, err := s.provider(st)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return p.Models(ctx)
}

// Status is what every user sees: whether the assistant is available and which models to pick from.
type Status struct {
	Enabled      bool     `json:"enabled"`
	DefaultModel string   `json:"default_model,omitempty"`
	Models       []string `json:"models"`
}

func (s *Service) Status(ctx context.Context) (*Status, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if !st.Enabled {
		return &Status{Models: []string{}}, nil
	}
	return &Status{Enabled: true, DefaultModel: st.DefaultModel, Models: st.allowedModels()}, nil
}

func systemPrompt(now time.Time, readOnly bool) string {
	var b strings.Builder
	b.WriteString(`You are the assistant built into AutoHost, a self-hosting panel that manages the user's Linux servers ("nodes"), their Docker containers and apps, backups, service monitors, alerts, networking and connectors.

Rules:
- Use the tools to get real data. Never invent IDs, names or numbers. To act on a node by name, call list_nodes first to get its ID.
- Do not rely on IDs remembered from earlier in the conversation: before changing something, call list_nodes (or the matching list tool) again and take the ID from that fresh result.
- Prefer the specific tools. When none fits, call search_endpoints right away, then get_endpoint_schema and execute_endpoint. Never say that something is not available, and never ask whether you should search, without searching first. search_endpoints only understands English keywords, whatever language the user writes in.
- Tools that change something are shown to the user for approval before they run: just call them, do not ask for permission in text first. If the user rejects an action, do not retry it.
- If a request is ambiguous (which node, which container, which action), ask a short question instead of guessing.
- Answer in the user's language, briefly. Use Markdown lists or tables for several items.
- To show the content of a Markdown (.md) file, put it in a code block whose language is markdown (fence it with four backticks if the file itself contains code blocks), so the panel renders it; other files go in a code block with their language.
`)
	if readOnly {
		b.WriteString("- READ-ONLY MODE: you can only read. If the user asks for a change, explain which action would be needed and that read-only mode must be turned off.\n")
	}
	fmt.Fprintf(&b, "\nCurrent time: %s.", now.UTC().Format(time.RFC3339))
	return b.String()
}

// contextWindow returns the messages sent to the model: the most recent ones, starting at a user
// message, with old tool results shortened.
func contextWindow(history []Message) []Message {
	start := 0
	if len(history) > contextMessages {
		start = len(history) - contextMessages
	}
	for start < len(history) && history[start].Role != "user" {
		start++
	}
	if start == len(history) {
		start = 0
	}
	out := make([]Message, 0, len(history)-start)
	for i, m := range history[start:] {
		if m.Role == "tool" && len(history)-start-i > recentToolMessages && len(m.Content) > oldToolResultChars {
			m.Content = `{"note":"old result removed to save space; call the tool again for current data"}`
		}
		out = append(out, m)
	}
	return out
}

func validateHistory(req *ChatRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("%w: messages is empty", ErrInvalid)
	}
	if len(req.Messages) > maxMessages {
		return fmt.Errorf("%w: the conversation is too long, start a new one", ErrInvalid)
	}
	size := 0
	for _, m := range req.Messages {
		switch m.Role {
		case "user", "assistant", "tool":
		default:
			return fmt.Errorf("%w: role %q is not allowed", ErrInvalid, m.Role)
		}
		size += len(m.Content)
		for _, c := range m.ToolCalls {
			size += len(c.Function.Arguments)
		}
	}
	if size > maxHistoryBytes {
		return fmt.Errorf("%w: the conversation is too long, start a new one", ErrInvalid)
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" && !(last.Role == "assistant" && len(last.ToolCalls) > 0) {
		return fmt.Errorf("%w: the last message must be from the user, or tool calls to approve", ErrInvalid)
	}
	return nil
}

func (s *Service) tool(name string) *Tool {
	for _, t := range s.tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// callInfo describes a tool call for the client.
type callInfo struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Effect    string         `json:"effect"`
	Summary   string         `json:"summary"`
	// Target names the node the call acts on, looked up in the API, so the user can check what the
	// model's IDs point to before approving.
	Target string `json:"target,omitempty"`
	// TargetWarning is set when the node does not exist or the name is ambiguous.
	TargetWarning string `json:"target_warning,omitempty"`
}

func (s *Service) describe(c ToolCall) (callInfo, *Tool, error) {
	info := callInfo{ID: c.ID, Name: c.Function.Name, Arguments: map[string]any{}, Effect: EffectRead}
	t := s.tool(c.Function.Name)
	if err := json.Unmarshal([]byte(c.Function.Arguments), &info.Arguments); err != nil {
		info.Summary = c.Function.Name
		return info, t, fmt.Errorf("%w: arguments are not valid JSON", errToolInput)
	}
	if info.Arguments == nil {
		info.Arguments = map[string]any{}
	}
	if t == nil {
		info.Summary = c.Function.Name
		return info, nil, fmt.Errorf("%w: unknown tool %q", errToolInput, c.Function.Name)
	}
	info.Effect = effectOf(t, info.Arguments, s.index)
	if t.summary != nil {
		info.Summary = t.summary(info.Arguments)
	} else {
		args, _ := json.Marshal(info.Arguments)
		info.Summary = t.Name + " " + string(args)
	}
	return info, t, nil
}

// runCall executes one tool call and returns the tool message for the model.
func (s *Service) runCall(ctx context.Context, env *toolEnv, c ToolCall, readOnly bool, emit func(Event)) Message {
	info, t, err := s.describe(c)
	var result any
	ok := false
	switch {
	case err != nil:
		result = map[string]string{"error": err.Error()}
	case readOnly && info.Effect != EffectRead:
		result = map[string]string{"error": "read-only mode: this operation changes something and was not run"}
	default:
		res, runErr := t.run(ctx, env, info.Arguments)
		switch {
		case runErr == nil:
			result, ok = res, true
			if m, isMap := res.(map[string]any); isMap && m["error"] != nil {
				ok = false
			}
		case errors.Is(runErr, errToolInput), errors.Is(runErr, mcpserver.ErrBadArguments),
			errors.Is(runErr, mcpserver.ErrBlocked), errors.Is(runErr, mcpserver.ErrUnknownOperation):
			result = map[string]string{"error": runErr.Error()}
		default:
			log.Printf("[ERROR] assistant: tool %s: %v", c.Function.Name, runErr)
			result = map[string]string{"error": "the tool failed"}
		}
	}
	content := encodeResult(result)
	emit(Event{Type: "tool_result", Data: map[string]any{"id": c.ID, "name": c.Function.Name, "ok": ok, "result": preview(content)}})
	return Message{Role: "tool", ToolCallID: c.ID, Name: c.Function.Name, Content: content}
}

func preview(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "…"
	}
	return s
}

// Chat runs one turn and reports it through emit. It ends with a "done" event carrying the messages
// to append to the conversation, after a "confirm" event when calls wait for the user's approval.
func (s *Service) Chat(ctx context.Context, req ChatRequest, exec Exec, emit func(Event)) error {
	st, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	if !st.Enabled {
		return ErrNotConfigured
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = st.DefaultModel
	}
	if !slices.Contains(st.allowedModels(), model) {
		return fmt.Errorf("%w: model %q is not enabled", ErrInvalid, model)
	}
	if err := validateHistory(&req); err != nil {
		return err
	}
	provider, err := s.provider(st)
	if err != nil {
		return err
	}
	env := &toolEnv{exec: exec, index: s.index, now: s.now}
	history := append([]Message{}, req.Messages...)
	var added []Message
	appendMsg := func(m Message) {
		history = append(history, m)
		added = append(added, m)
	}

	// Resume a turn that stopped for approval: run the approved calls, reject the others.
	if last := history[len(history)-1]; last.Role == "assistant" {
		for _, c := range last.ToolCalls {
			info, _, derr := s.describe(c)
			if derr == nil && info.Effect != EffectRead && !req.Approvals[c.ID] {
				content := encodeResult(map[string]string{"error": "the user rejected this action; do not retry it unless they ask"})
				emit(Event{Type: "tool_result", Data: map[string]any{"id": c.ID, "name": c.Function.Name, "ok": false, "rejected": true, "result": content}})
				appendMsg(Message{Role: "tool", ToolCallID: c.ID, Name: c.Function.Name, Content: content})
				continue
			}
			appendMsg(s.runCall(ctx, env, c, req.ReadOnly, emit))
		}
	}

	specList := specs(s.tools, req.ReadOnly)
	for step := 0; step < maxSteps; step++ {
		messages := append([]Message{{Role: "system", Content: systemPrompt(s.now(), req.ReadOnly)}}, contextWindow(history)...)
		msg, err := provider.Stream(ctx, completionRequest{
			Model: model, Messages: messages, Tools: specList, ReasoningEffort: st.ReasoningEffort,
		}, streamHandlers{
			onContent:   func(t string) { emit(Event{Type: "delta", Data: map[string]string{"text": t}}) },
			onReasoning: func(t string) { emit(Event{Type: "reasoning", Data: map[string]string{"text": t}}) },
		})
		if err != nil {
			if len(added) > 0 {
				emit(Event{Type: "done", Data: map[string]any{"messages": added}})
			}
			return err
		}
		appendMsg(msg)
		if len(msg.ToolCalls) == 0 {
			emit(Event{Type: "done", Data: map[string]any{"messages": added}})
			return nil
		}

		var pending []callInfo
		for _, c := range msg.ToolCalls {
			info, _, _ := s.describe(c)
			emit(Event{Type: "tool_call", Data: info})
			if info.Effect != EffectRead && !req.ReadOnly {
				pending = append(pending, info)
			}
		}
		if len(pending) > 0 {
			for i := range pending {
				env.annotateTarget(ctx, &pending[i])
			}
			emit(Event{Type: "confirm", Data: map[string]any{"calls": pending}})
			emit(Event{Type: "done", Data: map[string]any{"messages": added, "pending": true}})
			return nil
		}
		for _, c := range msg.ToolCalls {
			appendMsg(s.runCall(ctx, env, c, req.ReadOnly, emit))
		}
	}
	stop := Message{Role: "assistant", Content: fmt.Sprintf("(Stopped after %d steps. Ask me to continue if needed.)", maxSteps)}
	emit(Event{Type: "delta", Data: map[string]string{"text": stop.Content}})
	appendMsg(stop)
	emit(Event{Type: "done", Data: map[string]any{"messages": added}})
	return nil
}

// nodeRef finds the node a call refers to: node_id for direct tools, the nodeId/nodeID/node_id
// parameter or body field for execute_endpoint.
func nodeRef(args map[string]any) string {
	if v, ok := args["node_id"].(string); ok {
		return strings.TrimSpace(v)
	}
	for _, key := range []string{"params", "body"} {
		m, _ := args[key].(map[string]any)
		for _, name := range []string{"nodeId", "nodeID", "node_id"} {
			if v, ok := m[name].(string); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// annotateTarget looks up what a pending call acts on (a node, a connector) and describes it for the
// approval card.
func (env *toolEnv) annotateTarget(ctx context.Context, info *callInfo) {
	if id, ok := info.Arguments["connector_id"].(string); ok && id != "" {
		res, err := env.exec(ctx, "getConnector", map[string]any{"id": id}, nil)
		if err != nil || res.Status >= 400 {
			info.TargetWarning = fmt.Sprintf("connector %s was not found or you cannot see it", id)
			return
		}
		c, _ := res.Body.(map[string]any)
		info.Target = fmt.Sprintf("connector %v", c["name"])
		if url, _ := c["url"].(string); url != "" { // only returned to users who manage it
			info.Target += fmt.Sprintf(" (%v %s)", c["method"], url)
		}
		return
	}
	ref := nodeRef(info.Arguments)
	if ref == "" {
		return
	}
	id, err := env.resolveNode(ctx, ref)
	if err != nil {
		info.TargetWarning = fmt.Sprintf("no single node matches %q", ref)
		return
	}
	res, err := env.exec(ctx, "getNode", map[string]any{"id": id}, nil)
	if err != nil || res.Status >= 400 {
		info.TargetWarning = fmt.Sprintf("node %s was not found or you cannot see it", id)
		return
	}
	node, _ := res.Body.(map[string]any)
	host, _ := node["hostname"].(string)
	info.Target = fmt.Sprintf("node %s (%s)", host, id)
}
