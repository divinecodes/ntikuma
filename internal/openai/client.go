// Package openai talks to OpenAI's Responses API over the same Request and
// Result types as the openrouter client, so the chat service, tool loop and
// Continue work unchanged.
//
// Wire format from the Responses API reference (2026-10-30): POST
// {base}/responses with a bearer key; the stream is response.created, output
// items (response.output_item.added/.done, text in response.output_text.delta,
// refusals in response.refusal.delta), then one of response.completed,
// response.incomplete or response.failed. Current models can't call tools
// over Chat Completions while they reason, which is why this client exists.
// Requests are stateless (store: false): a tool step's output items, its
// encrypted reasoning included, go back unchanged on the next request.
package openai

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
	DefaultBaseURL = "https://api.openai.com/v1"
	// maxContinuationBytes bounds the output items kept for the tool loop.
	maxContinuationBytes = 2 * 1024 * 1024
)

// Client streams answers from a Responses API endpoint.
type Client struct {
	BaseURL   string // with /v1
	APIKey    string
	MaxTokens int // max_output_tokens; 0 leaves it to the model
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

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // a string, or parts for a question with images
}

type functionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type functionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type tool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

type requestBody struct {
	Model           string            `json:"model"`
	Instructions    string            `json:"instructions,omitempty"`
	Input           []json.RawMessage `json:"input"`
	Tools           []tool            `json:"tools,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Stream          bool              `json:"stream"`
	Store           bool              `json:"store"`
}

// toInput maps Chat Completions turns to Responses input items: system
// messages become the instructions, tool replies function_call_output items.
// An assistant turn this client produced goes back as the items it came as.
func toInput(msgs []openrouter.Message) (string, []json.RawMessage, error) {
	var instructions []string
	var out []json.RawMessage
	add := func(item any) error {
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		out = append(out, raw)
		return nil
	}
	for _, m := range msgs {
		var err error
		switch {
		case m.Role == "system":
			instructions = append(instructions, m.Content)
		case m.Role == "tool":
			err = add(functionCallOutput{Type: "function_call_output", CallID: m.ToolCallID, Output: m.Content})
		case m.Role == "assistant" && len(m.ReasoningDetails) > 0:
			out = append(out, m.ReasoningDetails...)
		case m.Role == "assistant":
			if strings.TrimSpace(m.Content) != "" {
				err = add(inputMessage{Role: "assistant", Content: m.Content})
			}
			for _, call := range m.ToolCalls {
				if err == nil {
					err = add(functionCall{Type: "function_call", CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
				}
			}
		case len(m.Images) > 0:
			parts := []contentPart{{Type: "input_text", Text: m.Content}}
			for _, image := range m.Images {
				if !strings.HasPrefix(image, "data:image/") {
					return "", nil, errors.New("openai: screenshots must be image data URLs")
				}
				parts = append(parts, contentPart{Type: "input_image", ImageURL: image, Detail: "auto"})
			}
			err = add(inputMessage{Role: m.Role, Content: parts})
		default:
			err = add(inputMessage{Role: m.Role, Content: m.Content})
		}
		if err != nil {
			return "", nil, err
		}
	}
	return strings.Join(instructions, "\n\n"), out, nil
}

type item struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type event struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`
	Item        json.RawMessage `json:"item"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	Response    struct {
		Model             string `json:"model"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

// incompleteReasons maps why a response stopped early to the Chat
// Completions values the tool loop and Continue already understand.
var incompleteReasons = map[string]string{
	"max_output_tokens": "length",
	"content_filter":    "content_filter",
}

// Stream sends req to its first model and calls onDelta with answer text as
// it arrives. Later models are the caller's to try.
func (c *Client) Stream(ctx context.Context, req openrouter.Request, onDelta func(string)) (res openrouter.Result, err error) {
	if len(req.Models) == 0 {
		return openrouter.Result{}, errors.New("openai: no model configured")
	}
	instructions, input, err := toInput(req.Messages)
	if err != nil {
		return openrouter.Result{}, err
	}
	body := requestBody{Model: req.Models[0], Instructions: instructions, Input: input, MaxOutputTokens: c.MaxTokens, Stream: true}
	// Web search and fetch are OpenRouter's server tools. Strict mode would
	// reject the local tools' optional arguments.
	for _, t := range req.Tools {
		if t.Type == "function" && t.Function != nil {
			schema := t.Function.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			body.Tools = append(body.Tools, tool{Type: "function", Name: t.Function.Name, Description: t.Function.Description, Parameters: schema})
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return openrouter.Result{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/responses", bytes.NewReader(payload))
	if err != nil {
		return openrouter.Result{}, err
	}
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
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
		if err == nil && (res.FinishReason == "length" || res.FinishReason == "content_filter" || res.FinishReason == "refusal") {
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

	continuation := 0
	hasCalls, refused, complete := false, false, false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() && !complete {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // event names, comments and separators
		}
		var ev event
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev); err != nil {
			return res, fmt.Errorf("openai: decode stream: %w", err)
		}
		switch ev.Type {
		case "response.created":
			res.Model = ev.Response.Model
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
			if onDelta != nil && ev.Delta != "" {
				onDelta(ev.Delta)
			}
		case "response.refusal.delta":
			// A refusal is shown as the answer, then ends it with FinishError.
			refused = true
			text.WriteString(ev.Delta)
			if onDelta != nil && ev.Delta != "" {
				onDelta(ev.Delta)
			}
		case "response.output_item.done":
			if len(ev.Item) > maxContinuationBytes-continuation {
				return res, errors.New("openai: reasoning state is too large")
			}
			continuation += len(ev.Item)
			var it item
			if err := json.Unmarshal(ev.Item, &it); err != nil {
				return res, fmt.Errorf("openai: decode item: %w", err)
			}
			if it.Type == "function_call" {
				hasCalls = true
				if err := calls.Add(ev.OutputIndex, it.CallID, it.Name, it.Arguments); err != nil {
					return res, err
				}
			}
			res.ReasoningDetails = append(res.ReasoningDetails, ev.Item)
		case "response.completed":
			complete = true
			res.FinishReason = "stop"
			if hasCalls {
				res.FinishReason = "tool_calls"
			}
			if refused {
				res.FinishReason = "refusal"
			}
		case "response.incomplete":
			complete = true
			res.FinishReason = "incomplete"
			if d := ev.Response.IncompleteDetails; d != nil {
				res.FinishReason = d.Reason
				if mapped, ok := incompleteReasons[d.Reason]; ok {
					res.FinishReason = mapped
				}
			}
			if res.FinishReason != "length" && res.FinishReason != "content_filter" {
				return res, &openrouter.APIError{Code: res.FinishReason, Message: "the answer stopped early (" + res.FinishReason + ")"}
			}
		case "response.failed":
			if e := ev.Response.Error; e != nil && e.Message != "" {
				return res, &openrouter.APIError{Code: e.Code, Message: e.Message}
			}
			return res, &openrouter.APIError{Message: "the response failed"}
		case "error":
			if ev.Message != "" {
				return res, &openrouter.APIError{Code: ev.Code, Message: ev.Message}
			}
			return res, &openrouter.APIError{Message: "the stream reported an error"}
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
		return res, fmt.Errorf("openai: %w", openrouter.ErrIncomplete)
	}
	return res, nil
}

func decodeHTTPError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Code    any    `json:"code"`
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
	code := e.Error.Code
	if code == nil && e.Error.Type != "" {
		code = e.Error.Type
	}
	return &openrouter.APIError{Status: resp.StatusCode, Code: code, Message: msg}
}
