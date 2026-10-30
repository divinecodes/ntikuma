// Package anthropic talks to the Claude Messages API (and servers that
// copy it) over the same Request and Result types as the openrouter client,
// so the chat service, tool loop and Continue work unchanged.
//
// Wire format from the Messages API reference (2026-10-30): POST /v1/messages
// with x-api-key and anthropic-version; the stream is message_start, then
// content_block_start/_delta/_stop per block, message_delta with the stop
// reason, and message_stop. Current models think by default and can't turn it
// off, so a response may start with thinking blocks whose signature binds
// them to the conversation; they go back unchanged on the tool-call turn.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ntikuma/internal/openrouter"
)

const (
	DefaultBaseURL = "https://api.anthropic.com"
	apiVersion     = "2026-10-21"
	// Thinking counts toward max_tokens, so leave room for it and the answer.
	defaultMaxTokens = 32000
	// maxContinuationBytes bounds the thinking state kept for the tool loop.
	maxContinuationBytes = 2 * 1024 * 1024
	maxModelListBytes    = 4 << 20
)

// Client streams answers from a Messages API endpoint.
type Client struct {
	BaseURL   string // without /v1
	APIKey    string // optional for servers on this machine
	MaxTokens int    // 0 uses the default
	HTTP      *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		// No overall timeout: answers stream for minutes and stop via the context.
		HTTP: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 60 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				ForceAttemptHTTP2:     true,
			},
			CheckRedirect: openrouter.SameOriginRedirects,
		},
	}
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    *imageSource    `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   *string         `json:"content,omitempty"`
}

type message struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type requestBody struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
	Stream    bool      `json:"stream"`
	Tools     []tool    `json:"tools,omitempty"`
}

func isThinking(raw json.RawMessage) bool {
	var head struct{ Type string }
	_ = json.Unmarshal(raw, &head)
	return head.Type == "thinking" || head.Type == "redacted_thinking"
}

func hasThinking(msgs []openrouter.Message) bool {
	for _, m := range msgs {
		for _, raw := range m.ReasoningDetails {
			if isThinking(raw) {
				return true
			}
		}
	}
	return false
}

// toMessages maps Chat Completions turns to Messages turns: system messages
// become the top-level system prompt, consecutive tool replies one user turn
// of tool_result blocks. An assistant turn this client produced goes back as
// it came, unless stripThinking leaves its thinking out.
func toMessages(msgs []openrouter.Message, stripThinking bool) (string, []message, error) {
	var system []string
	var out []message
	add := func(block any) error {
		raw, err := json.Marshal(block)
		if err != nil {
			return err
		}
		out[len(out)-1].Content = append(out[len(out)-1].Content, raw)
		return nil
	}
	toolTurn := false
	for _, m := range msgs {
		switch m.Role {
		case "system":
			system = append(system, m.Content)
			continue
		case "tool":
			if !toolTurn {
				out = append(out, message{Role: "user"})
				toolTurn = true
			}
			block := contentBlock{Type: "tool_result", ToolUseID: m.ToolCallID}
			if m.Content != "" {
				block.Content = &m.Content
			}
			if err := add(block); err != nil {
				return "", nil, err
			}
			continue
		}
		toolTurn = false
		out = append(out, message{Role: m.Role})
		switch {
		case m.Role == "assistant" && len(m.ReasoningDetails) > 0:
			for _, raw := range m.ReasoningDetails {
				if !(stripThinking && isThinking(raw)) {
					out[len(out)-1].Content = append(out[len(out)-1].Content, raw)
				}
			}
		case m.Role == "assistant":
			// The API refuses empty text blocks.
			if strings.TrimSpace(m.Content) != "" {
				if err := add(contentBlock{Type: "text", Text: m.Content}); err != nil {
					return "", nil, err
				}
			}
			for _, call := range m.ToolCalls {
				args := call.Function.Arguments
				if args == "" {
					args = "{}"
				}
				var input map[string]json.RawMessage
				if err := json.Unmarshal([]byte(args), &input); err != nil || input == nil {
					return "", nil, fmt.Errorf("anthropic: tool call %s has arguments that aren't a JSON object", call.ID)
				}
				if err := add(contentBlock{Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: json.RawMessage(args)}); err != nil {
					return "", nil, err
				}
			}
		default:
			for _, image := range m.Images {
				source, err := imageFrom(image)
				if err != nil {
					return "", nil, err
				}
				if err := add(contentBlock{Type: "image", Source: source}); err != nil {
					return "", nil, err
				}
			}
			if strings.TrimSpace(m.Content) != "" {
				if err := add(contentBlock{Type: "text", Text: m.Content}); err != nil {
					return "", nil, err
				}
			}
		}
		if len(out[len(out)-1].Content) == 0 {
			out = out[:len(out)-1]
		}
	}
	return strings.Join(system, "\n\n"), out, nil
}

// imageFrom turns a base64 data URL into an image source.
func imageFrom(dataURL string) (*imageSource, error) {
	meta, data, ok := strings.Cut(strings.TrimPrefix(dataURL, "data:"), ";base64,")
	if !ok || !strings.HasPrefix(dataURL, "data:") || !strings.HasPrefix(meta, "image/") {
		return nil, errors.New("anthropic: screenshots must be base64 data URLs")
	}
	return &imageSource{Type: "base64", MediaType: meta, Data: data}, nil
}

// Stream sends req to its first model and calls onDelta with answer text as
// it arrives. Later models are the caller's to try. If the API rejects a
// thinking block as bound to another conversation, it retries once without
// thinking, the API's documented recovery.
func (c *Client) Stream(ctx context.Context, req openrouter.Request, onDelta func(string)) (openrouter.Result, error) {
	res, err := c.stream(ctx, req, onDelta, false)
	var apiErr *openrouter.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest && strings.Contains(apiErr.Message, "signature") && strings.Contains(apiErr.Message, "thinking") && hasThinking(req.Messages) {
		return c.stream(ctx, req, onDelta, true)
	}
	return res, err
}

type event struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Model string `json:"model"`
	} `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// block is a content block being streamed.
type block struct {
	Type                string          `json:"type"`
	Text                string          `json:"text"`
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	Input               json.RawMessage `json:"input"`
	start               json.RawMessage
	thinking, signature strings.Builder
	arguments           strings.Builder
}

// finishReasons maps stop reasons to the Chat Completions values the tool
// loop and Continue already understand.
var finishReasons = map[string]string{
	"end_turn":                      "stop",
	"stop_sequence":                 "stop",
	"max_tokens":                    "length",
	"model_context_window_exceeded": "length",
	"tool_use":                      "tool_calls",
	"refusal":                       "refusal",
}

func (c *Client) stream(ctx context.Context, req openrouter.Request, onDelta func(string), stripThinking bool) (res openrouter.Result, err error) {
	if len(req.Models) == 0 {
		return openrouter.Result{}, errors.New("anthropic: no model configured")
	}
	system, msgs, err := toMessages(req.Messages, stripThinking)
	if err != nil {
		return openrouter.Result{}, err
	}
	body := requestBody{Model: req.Models[0], MaxTokens: c.MaxTokens, System: system, Messages: msgs, Stream: true}
	if body.MaxTokens <= 0 {
		body.MaxTokens = defaultMaxTokens
	}
	// Web search and fetch are OpenRouter's server tools.
	for _, t := range req.Tools {
		if t.Type == "function" && t.Function != nil {
			schema := t.Function.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			body.Tools = append(body.Tools, tool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: schema})
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return openrouter.Result{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return openrouter.Result{}, err
	}
	c.headers(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	var text strings.Builder
	var calls openrouter.ToolCallAccumulator
	defer func() {
		res.Text = text.String()
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			res.ToolCalls, err = calls.Finish(res.FinishReason)
		}
		if err == nil && (res.FinishReason == "length" || res.FinishReason == "refusal") {
			err = &openrouter.FinishError{Reason: res.FinishReason}
		}
	}()

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return res, decodeHTTPError(resp)
	}

	blocks := map[int]*block{}
	continuation := 0
	keep := func(size int) error {
		if size > maxContinuationBytes-continuation {
			return errors.New("anthropic: thinking state is too large")
		}
		continuation += size
		return nil
	}
	complete := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // event names, comments and separators
		}
		var ev event
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev); err != nil {
			return res, fmt.Errorf("anthropic: decode stream: %w", err)
		}
		switch ev.Type {
		case "message_start":
			res.Model = ev.Message.Model
		case "content_block_start":
			b := &block{start: ev.ContentBlock}
			if err := json.Unmarshal(ev.ContentBlock, b); err != nil {
				return res, fmt.Errorf("anthropic: decode block: %w", err)
			}
			blocks[ev.Index] = b
			switch b.Type {
			case "text":
				if b.Text != "" {
					text.WriteString(b.Text)
					if onDelta != nil {
						onDelta(b.Text)
					}
				}
			case "tool_use":
				if err := calls.Add(ev.Index, b.ID, b.Name, ""); err != nil {
					return res, err
				}
			case "thinking":
			default:
				// Redacted thinking and anything newer go back as they came.
				if err := keep(len(ev.ContentBlock)); err != nil {
					return res, err
				}
			}
		case "content_block_delta":
			b := blocks[ev.Index]
			if b == nil {
				return res, errors.New("anthropic: delta for a block that never started")
			}
			switch ev.Delta.Type {
			case "text_delta":
				b.Text += ev.Delta.Text
				text.WriteString(ev.Delta.Text)
				if onDelta != nil && ev.Delta.Text != "" {
					onDelta(ev.Delta.Text)
				}
			case "input_json_delta":
				b.arguments.WriteString(ev.Delta.PartialJSON)
				if err := calls.Add(ev.Index, "", "", ev.Delta.PartialJSON); err != nil {
					return res, err
				}
			case "thinking_delta", "signature_delta":
				if err := keep(len(ev.Delta.Thinking) + len(ev.Delta.Signature)); err != nil {
					return res, err
				}
				b.thinking.WriteString(ev.Delta.Thinking)
				b.signature.WriteString(ev.Delta.Signature)
			}
		case "content_block_stop":
			b := blocks[ev.Index]
			if b == nil {
				return res, errors.New("anthropic: stop for a block that never started")
			}
			raw, err := b.wire(&calls, ev.Index)
			if err != nil {
				return res, err
			}
			if raw != nil {
				res.ReasoningDetails = append(res.ReasoningDetails, raw)
			}
		case "message_delta":
			if reason := ev.Delta.StopReason; reason != "" {
				res.FinishReason = reason
				if mapped, ok := finishReasons[reason]; ok {
					res.FinishReason = mapped
				}
			}
		case "message_stop":
			complete = true
		case "error":
			if ev.Error != nil {
				return res, &openrouter.APIError{Code: ev.Error.Type, Message: ev.Error.Message}
			}
			return res, &openrouter.APIError{Message: "the stream reported an error"}
		}
		if complete {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, err
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if !complete {
		return res, fmt.Errorf("anthropic: %w", openrouter.ErrIncomplete)
	}
	return res, nil
}

// wire is the finished block as the API expects it back. A tool call that
// streamed no input has an empty object; one whose input isn't JSON (cut off
// by the output limit) isn't kept, and the tool loop never runs it.
func (b *block) wire(calls *openrouter.ToolCallAccumulator, index int) (json.RawMessage, error) {
	switch b.Type {
	case "text":
		if b.Text == "" {
			return nil, nil
		}
		return json.Marshal(contentBlock{Type: "text", Text: b.Text})
	case "thinking":
		return json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{"thinking", b.thinking.String(), b.signature.String()})
	case "tool_use":
		args := b.arguments.String()
		if args == "" {
			args = "{}"
			if len(b.Input) > 0 && string(b.Input) != "null" {
				args = string(b.Input)
			}
			if err := calls.Add(index, "", "", args); err != nil {
				return nil, err
			}
		}
		if !json.Valid([]byte(args)) {
			return nil, nil
		}
		return json.Marshal(contentBlock{Type: "tool_use", ID: b.ID, Name: b.Name, Input: json.RawMessage(args)})
	default:
		return b.start, nil
	}
}

func (c *Client) headers(r *http.Request) {
	if c.APIKey != "" {
		r.Header.Set("x-api-key", c.APIKey)
	}
	r.Header.Set("anthropic-version", apiVersion)
}

func decodeHTTPError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if msg == "" {
		msg = resp.Status
	}
	return &openrouter.APIError{Status: resp.StatusCode, Code: e.Error.Type, Message: msg}
}

// Models lists the endpoint's models, one page of up to 1000, for the
// Preferences suggestions.
func (c *Client) Models(ctx context.Context) ([]openrouter.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/v1/models?limit=1000", nil)
	if err != nil {
		return nil, err
	}
	c.headers(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeHTTPError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxModelListBytes {
		return nil, errors.New("the model list is too large")
	}
	var body struct {
		Data []struct {
			ID             string `json:"id"`
			DisplayName    string `json:"display_name"`
			MaxInputTokens int    `json:"max_input_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]openrouter.ModelInfo, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, openrouter.ModelInfo{ID: m.ID, Name: m.DisplayName, ContextLength: m.MaxInputTokens})
	}
	return out, nil
}
