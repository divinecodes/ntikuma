package openrouter

// Live server-tool probe, 2026-10-26, anthropic/claude-sonnet-5.5 via the
// configured Sonnet/GPT fallback chain: tool events streamed, but all 80 answer
// deltas arrived in a 13 ms burst after 17.3 s. Web requests therefore use JSON
// completions; the non-streaming comparison succeeded in 14.7 s. Citations were
// in delta.annotations for SSE and message.annotations for JSON. The final
// usage had server_tool_use_details.web_search_requests = 1, not the documented
// server_tool_use key. Both spellings are accepted below. These observations
// describe this provider/date, not every model. Sanitized captures are in testdata.
//
// Function-tool: SSE supplied index/id/name first, then three
// nonempty argument fragments, and finish_reason="tool_calls". SSE and JSON
// continuations both answered using a synthetic role:"tool" reply. Mixed Web
// and function JSON requests also worked; the continuation reported one search.
// That path returned signed reasoning_details, which must be replayed unchanged.
// Sanitized response fixtures and the probe limitations are documented in testdata.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const DefaultBaseURL = "https://openrouter.ai/api/v1"

type Message struct {
	Role             string            `json:"role"` // system, user, assistant, or tool
	Content          string            `json:"content"`
	Images           []string          `json:"-"` // PNG/JPEG data URLs, serialized after the text.
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	Reasoning        string            `json:"reasoning,omitempty"`
	ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
}

// MarshalJSON preserves string content for text-only messages and uses the
// Chat Completions content-part format when images are present.
func (m Message) MarshalJSON() ([]byte, error) {
	var content any = m.Content
	reasoning := m.Reasoning
	// Structured blocks carry signatures/opaque state; the plain field is a
	// fallback for providers that do not return those blocks, not a second copy.
	if len(m.ReasoningDetails) > 0 {
		reasoning = ""
	}
	type part struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url,omitempty"`
	}
	if len(m.Images) > 0 {
		parts := []part{{Type: "text", Text: m.Content}}
		for _, url := range m.Images {
			parts = append(parts, part{Type: "image_url", ImageURL: &struct {
				URL string `json:"url"`
			}{URL: url}})
		}
		content = parts
	} else if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) > 0 {
		content = nil
	}
	return json.Marshal(struct {
		Role             string            `json:"role"`
		Content          any               `json:"content"`
		ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
		ToolCallID       string            `json:"tool_call_id,omitempty"`
		Reasoning        string            `json:"reasoning,omitempty"`
		ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
	}{m.Role, content, m.ToolCalls, m.ToolCallID, reasoning, m.ReasoningDetails})
}

type Request struct {
	Models       []string // models in order of priority
	Messages     []Message
	SessionID    string //session ID to keep conversation on same provider
	Tools        []Tool `json:"tools,omitempty"`
	MaxToolCalls int    `json:"max_tool_calls,omitempty"`
	NonStreaming bool   `json:"-"`
}

type Function struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type Tool struct {
	Type       string         `json:"type"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Function   *Function      `json:"function,omitempty"`
}

const (
	WebSearchType = "openrouter:web_search"
	WebFetchType  = "openrouter:web_fetch"
)

func WebTools(maxResults, maxFetches int) []Tool {
	return []Tool{
		{Type: WebSearchType, Parameters: map[string]any{"max_results": maxResults}},
		{Type: WebFetchType, Parameters: map[string]any{"max_uses": maxFetches}},
	}
}

type Citation struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type Result struct {
	Model            string // the model that answered
	Text             string // the answer
	Fallback         bool   // if fallback model was used
	FinishReason     string // finished reason as reported
	Citations        []Citation
	Searches         *int       // nil means the provider did not report search usage.
	ToolCalls        []ToolCall // Only exposed after a successful, complete tool-call response.
	Reasoning        string
	ReasoningDetails []json.RawMessage // Opaque continuation state; never answer text.
}

// ErrIncomplete reports a stream that closed with neither a finish reason nor
// the [DONE] marker, so the text it returned is only part of the answer.
var ErrIncomplete = errors.New("the connection closed before the answer finished")

// FinishError reports an answer the provider ended early. OpenRouter
// normalizes finish_reason to tool_calls, stop, length, content_filter or
// error; the last three mean the text is cut short. Other values pass.
type FinishError struct{ Reason string }

func (e *FinishError) Error() string {
	switch e.Reason {
	case "length":
		return "the answer reached the model's output limit and was cut off"
	case "content_filter":
		return "the provider's content filter stopped the answer"
	default:
		return "the provider stopped the answer with an error"
	}
}

func finishError(reason string) error {
	switch reason {
	case "length", "content_filter", "error":
		return &FinishError{Reason: reason}
	}
	return nil
}

type APIError struct {
	Status  int
	Code    any
	Message string
}

func (e *APIError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("openrouter: %d: %s", e.Status, e.Message)
	}
	return "openrouter: " + e.Message
}

// Client talks to OpenRouter
type Client struct {
	BaseURL  string
	APIKey   string
	AppTitle string //AppTitle is sent as X-Title so the app shows up in OpenRouter
	HTTP     *http.Client
}

// New returns a client for apiKey
func New(apiKey string) *Client {
	return &Client{
		BaseURL:  DefaultBaseURL,
		APIKey:   apiKey,
		AppTitle: "Ntikuma",
		//no overall timeout: streams can run for minutes so we cancel via context
		HTTP: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		}},
	}
}

type chatBody struct {
	Model        string           `json:"model,omitempty"`
	Models       []string         `json:"models,omitempty"`
	Messages     []Message        `json:"messages"`
	Stream       bool             `json:"stream"`
	SessionID    string           `json:"session_id,omitempty"`
	Tools        []Tool           `json:"tools,omitempty"`
	MaxToolCalls int              `json:"max_tool_calls,omitempty"`
	Provider     *providerOptions `json:"provider,omitempty"`
}

type providerOptions struct {
	RequireParameters bool `json:"require_parameters"`
}

type annotation struct {
	Type     string   `json:"type"`
	Citation Citation `json:"url_citation"`
}

type responseMessage struct {
	Content          string            `json:"content"`
	Annotations      []annotation      `json:"annotations"`
	ToolCalls        []toolCallDelta   `json:"tool_calls"`
	Reasoning        string            `json:"reasoning"`
	ReasoningDetails []json.RawMessage `json:"reasoning_details"`
}

type toolUsage struct {
	Searches *int `json:"web_search_requests"`
}

type chunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Index        int             `json:"index"`
		Delta        responseMessage `json:"delta"`
		Message      responseMessage `json:"message"`
		FinishReason *string         `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		ServerToolUse        *toolUsage `json:"server_tool_use"`
		ServerToolUseDetails *toolUsage `json:"server_tool_use_details"`
	} `json:"usage"`
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Stream sends req and calls onDelta with each piece of text as it arrives.
// It returns when the stream ends, ctx is cancelled, or an error occurs. On
// cancellation it returns the partial Result together with ctx.Err().
func (c *Client) Stream(ctx context.Context, req Request, onDelta func(string)) (res Result, err error) {
	if len(req.Models) == 0 {
		return Result{}, errors.New("openrouter: no model configured")
	}
	if c.APIKey == "" {
		return Result{}, errors.New("openrouter: Missing API key")
	}
	var text, reasoning strings.Builder
	var calls toolCallAccumulator
	var reasoningBytes int
	defer func() {
		res.Text = text.String()
		res.Reasoning = reasoning.String()
		res.Fallback = res.Model != "" && !SameFamily(req.Models[0], res.Model)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			res.ToolCalls, err = calls.finish(res.FinishReason)
		}
		if err == nil {
			err = finishError(res.FinishReason)
		}
	}()

	models := req.Models
	if slices.ContainsFunc(req.Messages, func(m Message) bool { return len(m.Images) > 0 }) {
		// Never silently drop an image to satisfy a text-only fallback. Check
		// the current catalog because model capabilities and aliases change.
		catalogCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		catalog, err := c.cachedModels(catalogCtx)
		cancel()
		if err != nil {
			return Result{}, fmt.Errorf("couldn't check image support; try again: %w", err)
		}
		models = imageModels(models, catalog)
		if len(models) == 0 {
			return Result{}, errors.New("this chat contains screenshots; choose an image-capable model in Preferences or the model picker")
		}
	}
	body := chatBody{Messages: req.Messages, Stream: !req.NonStreaming, SessionID: req.SessionID, Tools: req.Tools, MaxToolCalls: req.MaxToolCalls}
	functionTools := slices.ContainsFunc(req.Tools, func(t Tool) bool { return t.Type == "function" })
	if functionTools {
		body.Provider = &providerOptions{RequireParameters: true}
	}
	if len(models) == 1 {
		body.Model = models[0]
	} else {
		// the models array tried in the order
		body.Models = models
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if req.NonStreaming {
		httpReq.Header.Set("Accept", "application/json")
	} else {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if c.AppTitle != "" {
		httpReq.Header.Set("X-Title", c.AppTitle)
	}

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	seen := map[string]int{}
	addCitations := func(annotations []annotation) {
		for _, a := range annotations {
			if a.Type != "url_citation" {
				continue
			}
			citation := a.Citation
			u, err := url.Parse(citation.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
				continue
			}
			if index, ok := seen[citation.URL]; ok {
				if res.Citations[index].Title == "" && citation.Title != "" {
					res.Citations[index].Title = citation.Title
				}
				continue
			}
			seen[citation.URL] = len(res.Citations)
			res.Citations = append(res.Citations, citation)
		}
	}
	consume := func(ch chunk) error {
		if ch.Model != "" {
			res.Model = ch.Model
		}
		if ch.Usage != nil {
			for _, usage := range []*toolUsage{ch.Usage.ServerToolUse, ch.Usage.ServerToolUseDetails} {
				if usage != nil && usage.Searches != nil && *usage.Searches >= 0 {
					res.Searches = usage.Searches
					break
				}
			}
		}
		for _, choice := range ch.Choices {
			if choice.Index != 0 {
				continue
			}
			addCitations(choice.Delta.Annotations)
			addCitations(choice.Message.Annotations)
			reasoningText := choice.Delta.Reasoning
			if reasoningText == "" && reasoning.Len() == 0 {
				reasoningText = choice.Message.Reasoning
			}
			details := choice.Delta.ReasoningDetails
			if len(details) == 0 && len(res.ReasoningDetails) == 0 {
				details = choice.Message.ReasoningDetails
			}
			size := len(reasoningText)
			for _, detail := range details {
				size += len(detail)
			}
			if size > maxReasoningBytes-reasoningBytes {
				return errors.New("openrouter: reasoning continuation state is too large")
			}
			reasoningBytes += size
			reasoning.WriteString(reasoningText)
			// Keep opaque blocks in wire order, including unknown provider fields.
			res.ReasoningDetails = append(res.ReasoningDetails, details...)
			content := choice.Delta.Content
			if content == "" && text.Len() == 0 {
				content = choice.Message.Content
			}
			if content != "" {
				text.WriteString(content)
				if onDelta != nil {
					onDelta(content)
				}
			}
			if err := calls.add(choice.Delta.ToolCalls, true); err != nil {
				return err
			}
			// A final full-message snapshot must not duplicate streamed arguments.
			if err := calls.snapshot(choice.Message.ToolCalls); err != nil {
				return err
			}
			if choice.FinishReason != nil {
				res.FinishReason = *choice.FinishReason
			}
		}
		if ch.Error != nil {
			return &APIError{Code: ch.Error.Code, Message: ch.Error.Message}
		}
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		// Failed generations can still carry citations or billable tool usage.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var ch chunk
		if json.Unmarshal(body, &ch) == nil {
			_ = consume(ch)
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return res, decodeHTTPError(resp)
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		var ch chunk
		const maxCompletionBytes = 16 * 1024 * 1024
		raw, decodeErr := io.ReadAll(io.LimitReader(resp.Body, maxCompletionBytes+1))
		if decodeErr == nil && len(raw) > maxCompletionBytes {
			decodeErr = errors.New("completion is too large")
		}
		if decodeErr == nil && functionTools && !utf8.Valid(raw) {
			decodeErr = errors.New("tool completion contains invalid UTF-8")
		}
		if decodeErr == nil {
			decodeErr = json.Unmarshal(raw, &ch)
		}
		if decodeErr != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			return res, fmt.Errorf("decode completion: %w", decodeErr)
		}
		if err := consume(ch); err != nil {
			return res, err
		}
		return res, nil
	}
	if req.NonStreaming && !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return res, errors.New("openrouter: expected a JSON completion")
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	complete := false
	for scanner.Scan() {
		line := scanner.Text()
		// Blank lines separate events, lines starting with ":" are comments
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			complete = true
			break
		}
		var ch chunk
		if functionTools && !utf8.ValidString(data) {
			return res, errors.New("openrouter: tool stream contains invalid UTF-8")
		}
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			if functionTools {
				return res, fmt.Errorf("decode tool stream: %w", err)
			}
			continue
		}
		if err := consume(ch); err != nil {
			return res, err
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
	if !complete && (len(calls.calls) > 0 || res.FinishReason == "tool_calls") {
		return res, fmt.Errorf("openrouter: incomplete tool-call stream: %w", io.ErrUnexpectedEOF)
	}
	// A finish reason is enough to call the answer whole; some proxies drop the
	// end marker. With neither, the connection closed mid-answer.
	if !complete && res.FinishReason == "" {
		return res, fmt.Errorf("openrouter: %w", ErrIncomplete)
	}
	return res, nil
}

func decodeHTTPError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var e struct {
		Error struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if msg == "" {
		msg = resp.Status
	}
	return &APIError{Status: resp.StatusCode, Code: e.Error.Code, Message: msg}
}

// SameFamily reports whether resolved (the model OpenRouter says answered) is
// the model that was requested. It understands ~vendor/family-latest aliases
// and dated variants such as "openai/gpt-5.1-2025-11-13".
func SameFamily(requested, resolved string) bool {
	req := strings.TrimPrefix(requested, "~")
	req = strings.TrimSuffix(req, "-latest")
	req = strings.TrimSuffix(req, ":free")
	return resolved == requested || strings.HasPrefix(resolved, req)
}

type ModelInfo struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ContextLength   int      `json:"contextLength"`
	InputModalities []string `json:"inputModalities,omitempty"`
}

func imageModels(models []string, catalog []ModelInfo) []string {
	var out []string
	for _, id := range models {
		for _, m := range catalog {
			alias := strings.HasPrefix(id, "~") || strings.HasSuffix(id, "-latest")
			if (id == m.ID || (alias && SameFamily(id, m.ID))) && slices.Contains(m.InputModalities, "image") {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// Image requests check the catalog on every model step, and the client is
// rebuilt for each answer, so successful lookups are shared for a while.
const catalogTTL = 10 * time.Minute

var catalogCache struct {
	sync.Mutex
	entries map[string]cachedCatalog
}

type cachedCatalog struct {
	models  []ModelInfo
	fetched time.Time
}

func (c *Client) cachedModels(ctx context.Context) ([]ModelInfo, error) {
	key := strings.TrimRight(c.BaseURL, "/")
	catalogCache.Lock()
	entry, ok := catalogCache.entries[key]
	catalogCache.Unlock()
	if ok && time.Since(entry.fetched) < catalogTTL {
		return entry.models, nil
	}
	models, err := c.Models(ctx)
	if err != nil {
		return nil, err
	}
	catalogCache.Lock()
	if catalogCache.entries == nil {
		catalogCache.entries = make(map[string]cachedCatalog)
	}
	catalogCache.entries[key] = cachedCatalog{models: models, fetched: time.Now()}
	catalogCache.Unlock()
	return models, nil
}

// Models lists the models OpenRouter currently offers. No key is needed.
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeHTTPError(resp)
	}
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			Architecture  struct {
				InputModalities []string `json:"input_modalities"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, ModelInfo{ID: m.ID, Name: m.Name, ContextLength: m.ContextLength, InputModalities: m.Architecture.InputModalities})
	}
	return out, nil
}
