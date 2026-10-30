package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"ntikuma/internal/openrouter"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type sent struct {
	path   string
	header http.Header
	body   map[string]json.RawMessage
}

// server replies to each request in turn and records what it was sent.
func server(t *testing.T, replies ...func(http.ResponseWriter)) (*Client, func() []sent) {
	t.Helper()
	var mu sync.Mutex
	var got []sent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		step := len(got)
		got = append(got, sent{r.URL.RequestURI(), r.Header.Clone(), body})
		mu.Unlock()
		if step >= len(replies) {
			t.Errorf("unexpected request %d", step)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		replies[step](w)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "fake-anthropic-key"), func() []sent { mu.Lock(); defer mu.Unlock(); return append([]sent(nil), got...) }
}

func stream(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}
}

func status(code int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}
}

// events builds a stream from data payloads, naming each event by its type.
func events(payloads ...string) string {
	var b strings.Builder
	for _, p := range payloads {
		var head struct{ Type string }
		_ = json.Unmarshal([]byte(p), &head)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", head.Type, p)
	}
	return b.String()
}

const (
	start   = `{"type":"message_start","message":{"model":"claude-opus-5-5","content":[]}}`
	textOn  = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	textOff = `{"type":"content_block_stop","index":0}`
	stopMsg = `{"type":"message_stop"}`
)

func text(s string) string {
	return `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + quote(s) + `}}`
}

func finish(reason string) string {
	return `{"type":"message_delta","delta":{"stop_reason":"` + reason + `"}}`
}

func quote(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func demoTool() openrouter.Tool {
	return openrouter.Tool{Type: "function", Function: &openrouter.Function{Name: "get_demo_fact", Description: "A fact", Parameters: map[string]any{"type": "object", "properties": map[string]any{"topic": map[string]any{"type": "string"}}}}}
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func TestRequestsUseTheMessagesFormat(t *testing.T) {
	c, got := server(t, stream(fixture(t, "text-stream.sse")))
	req := openrouter.Request{
		Models:    []string{"claude-opus-5-5", "claude-sonnet-5-5"},
		SessionID: "chat",
		Messages: []openrouter.Message{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "What's this?", Images: []string{"data:image/jpeg;base64,/9j/4AAQ"}},
			{Role: "assistant", Content: "A logo."},
			{Role: "user", Content: "Look it up."},
			{Role: "assistant", ToolCalls: []openrouter.ToolCall{
				{ID: "toolu_1", Type: "function", Function: openrouter.FunctionCall{Name: "get_demo_fact", Arguments: `{"topic":"logo"}`}},
				{ID: "toolu_2", Type: "function", Function: openrouter.FunctionCall{Name: "get_demo_fact", Arguments: `{}`}},
			}},
			{Role: "tool", ToolCallID: "toolu_1", Content: "a fact"},
			{Role: "tool", ToolCallID: "toolu_2", Content: ""},
		},
		Tools:        append(openrouter.WebTools(5, 3), demoTool()),
		MaxToolCalls: 6,
	}
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	r := got()[0]
	if r.path != "/v1/messages" {
		t.Errorf("path %s", r.path)
	}
	if r.header.Get("x-api-key") != "fake-anthropic-key" || r.header.Get("anthropic-version") != "2026-10-21" || r.header.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", r.header)
	}
	// No bearer token, and no beta an Anthropic-compatible server may not know.
	if r.header.Get("Authorization") != "" || r.header.Get("anthropic-beta") != "" {
		t.Errorf("extra headers %v", r.header)
	}
	for field := range r.body {
		switch field {
		case "model", "max_tokens", "system", "messages", "stream", "tools":
		default:
			t.Errorf("sent field %q", field)
		}
	}
	if string(r.body["model"]) != `"claude-opus-5-5"` || string(r.body["max_tokens"]) != "32000" || string(r.body["stream"]) != "true" || string(r.body["system"]) != `"Be brief."` {
		t.Errorf("body %s %s %s %s", r.body["model"], r.body["max_tokens"], r.body["stream"], r.body["system"])
	}

	type block struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Content   *string         `json:"content"`
		Source    *struct {
			Type, MediaType, Data string
		} `json:"source"`
	}
	type message struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	msgs := decode[[]message](t, r.body["messages"])
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	// Consecutive tool replies are one user turn, so roles alternate.
	if strings.Join(roles, " ") != "user assistant user assistant user" {
		t.Fatalf("roles %v", roles)
	}
	first := msgs[0].Content
	if len(first) != 2 || first[0].Type != "image" || first[0].Source == nil || first[1].Type != "text" || first[1].Text != "What's this?" {
		t.Fatalf("user turn %+v", first)
	}
	raw := decode[[]map[string]any](t, decode[[]map[string]json.RawMessage](t, r.body["messages"])[0]["content"])
	if src := raw[0]["source"].(map[string]any); src["type"] != "base64" || src["media_type"] != "image/jpeg" || src["data"] != "/9j/4AAQ" {
		t.Errorf("image source %v", src)
	}
	calls := msgs[3].Content
	// Empty text blocks are never sent.
	if len(calls) != 2 || calls[0].Type != "tool_use" || calls[0].ID != "toolu_1" || calls[0].Name != "get_demo_fact" || string(calls[0].Input) != `{"topic":"logo"}` || string(calls[1].Input) != `{}` {
		t.Fatalf("tool calls %+v", calls)
	}
	results := msgs[4].Content
	if len(results) != 2 || results[0].Type != "tool_result" || results[0].ToolUseID != "toolu_1" || results[0].Content == nil || *results[0].Content != "a fact" || results[1].ToolUseID != "toolu_2" || results[1].Content != nil {
		t.Fatalf("tool results %+v", results)
	}

	// Only function tools; web search belongs to OpenRouter.
	tools := decode[[]map[string]json.RawMessage](t, r.body["tools"])
	if len(tools) != 1 || string(tools[0]["name"]) != `"get_demo_fact"` || string(tools[0]["description"]) != `"A fact"` || !strings.Contains(string(tools[0]["input_schema"]), `"topic"`) {
		t.Fatalf("tools %s", r.body["tools"])
	}
	if _, ok := tools[0]["type"]; ok {
		t.Error("custom tools carry no type")
	}
}

func TestMaxTokensAndAKeylessServer(t *testing.T) {
	c, got := server(t, stream(fixture(t, "text-stream.sse")))
	c.APIKey, c.MaxTokens = "", 4096
	if _, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil); err != nil {
		t.Fatal(err)
	}
	r := got()[0]
	if string(r.body["max_tokens"]) != "4096" || r.header.Get("x-api-key") != "" {
		t.Fatalf("max_tokens %s, key %q", r.body["max_tokens"], r.header.Get("x-api-key"))
	}
	if _, ok := r.body["system"]; ok {
		t.Error("sent an empty system prompt")
	}
}

func TestToolArgumentsThatArentAnObjectAreRefused(t *testing.T) {
	for _, args := range []string{"{bad", "[1]", "null"} {
		c, got := server(t)
		req := openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{
			{Role: "user", Content: "q"},
			{Role: "assistant", ToolCalls: []openrouter.ToolCall{{ID: "toolu_1", Type: "function", Function: openrouter.FunctionCall{Name: "demo", Arguments: args}}}},
			{Role: "tool", ToolCallID: "toolu_1", Content: "error"},
		}}
		if _, err := c.Stream(context.Background(), req, nil); err == nil {
			t.Fatalf("%s: guessed the arguments", args)
		}
		if len(got()) != 0 {
			t.Fatalf("%s: sent the request", args)
		}
	}
}

func TestEachToolRoundIsItsOwnTurn(t *testing.T) {
	c, got := server(t, stream(fixture(t, "text-stream.sse")))
	call := func(id string) []openrouter.ToolCall {
		return []openrouter.ToolCall{{ID: id, Type: "function", Function: openrouter.FunctionCall{Name: "demo", Arguments: "{}"}}}
	}
	req := openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: call("toolu_1")},
		{Role: "tool", ToolCallID: "toolu_1", Content: "one"},
		{Role: "assistant", ToolCalls: call("toolu_2")},
		{Role: "tool", ToolCallID: "toolu_2", Content: "two"},
	}}
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	var msgs []message
	if err := json.Unmarshal(got()[0].body["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	var shape []string
	for _, m := range msgs {
		shape = append(shape, fmt.Sprintf("%s:%d", m.Role, len(m.Content)))
	}
	if strings.Join(shape, " ") != "user:1 assistant:1 user:1 assistant:1 user:1" {
		t.Fatalf("turns %v", shape)
	}
}

func TestAToolCallCutOffByTheLimitNeverRuns(t *testing.T) {
	body := events(start,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"demo","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \"/ho"}}`,
		`{"type":"content_block_stop","index":0}`, finish("max_tokens"), stopMsg)
	c, _ := server(t, stream(body))
	res, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "did not complete") || len(res.ToolCalls) != 0 || len(res.ReasoningDetails) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestTextStreams(t *testing.T) {
	c, _ := server(t, stream(fixture(t, "text-stream.sse")))
	var deltas []string
	res, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"claude-opus-5-5"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Paris is the capital." || strings.Join(deltas, "|") != "Paris is |the capital." || res.Model != "claude-opus-5-5" || res.FinishReason != "stop" || res.Fallback {
		t.Fatalf("%+v %q", res, deltas)
	}
	// Thinking text is never answer text or stored reasoning.
	if res.Reasoning != "" || len(res.ToolCalls) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestToolCallsGoBackWithTheirThinking(t *testing.T) {
	c, got := server(t, stream(fixture(t, "tool-stream.sse")), stream(fixture(t, "text-stream.sse")))
	req := openrouter.Request{Models: []string{"claude-opus-5-5"}, Messages: []openrouter.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "q"}}, Tools: []openrouter.Tool{demoTool()}}
	res, err := c.Stream(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := openrouter.ToolCall{ID: "toolu_fixture", Type: "function", Function: openrouter.FunctionCall{Name: "get_demo_fact", Arguments: `{"topic": "tides"}`}}
	if res.FinishReason != "tool_calls" || res.Text != "Let me look." || len(res.ToolCalls) != 1 || res.ToolCalls[0] != want {
		t.Fatalf("%+v", res)
	}

	// The tool loop appends the turn as it does for every provider.
	req.Messages = append(req.Messages,
		openrouter.Message{Role: "assistant", Content: res.Text, ToolCalls: res.ToolCalls, Reasoning: res.Reasoning, ReasoningDetails: res.ReasoningDetails},
		openrouter.Message{Role: "tool", ToolCallID: "toolu_fixture", Content: "fact"})
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	msgs := decode[[]map[string]json.RawMessage](t, got()[1].body["messages"])
	blocks := decode[[]map[string]any](t, msgs[1]["content"])
	if len(blocks) != 3 {
		t.Fatalf("assistant turn %s", msgs[1]["content"])
	}
	// Sent back unchanged and in order: the signature binds the reasoning.
	if blocks[0]["type"] != "thinking" || blocks[0]["signature"] != "fixture-signature-1" || blocks[0]["thinking"] != "" {
		t.Errorf("thinking %v", blocks[0])
	}
	if blocks[1]["type"] != "text" || blocks[1]["text"] != "Let me look." {
		t.Errorf("text %v", blocks[1])
	}
	if blocks[2]["type"] != "tool_use" || blocks[2]["id"] != "toolu_fixture" || fmt.Sprint(blocks[2]["input"]) != "map[topic:tides]" {
		t.Errorf("tool_use %v", blocks[2])
	}
}

func TestRedactedThinkingIsKeptAsSent(t *testing.T) {
	body := events(start,
		`{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"opaque-fixture"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"demo"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"demo","input":{"topic":"tides"}}}`,
		`{"type":"content_block_stop","index":2}`,
		finish("tool_use"), stopMsg)
	c, _ := server(t, stream(body))
	res, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil)
	// No input fragments: an empty object, or the input the block started with.
	if err != nil || len(res.ToolCalls) != 2 || res.ToolCalls[0].Function.Arguments != "{}" || res.ToolCalls[1].Function.Arguments != `{"topic":"tides"}` {
		t.Fatalf("%+v %v", res, err)
	}
	if len(res.ReasoningDetails) != 3 || string(res.ReasoningDetails[0]) != `{"type":"redacted_thinking","data":"opaque-fixture"}` {
		t.Fatalf("details %s", res.ReasoningDetails)
	}
}

func TestStopReasons(t *testing.T) {
	for _, tc := range []struct {
		reason, finish string
		cut            string // FinishError reason, or "" for none
	}{
		{"end_turn", "stop", ""},
		{"stop_sequence", "stop", ""},
		{"max_tokens", "length", "length"},
		{"model_context_window_exceeded", "length", "length"},
		{"refusal", "refusal", "refusal"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			c, _ := server(t, stream(events(start, textOn, text("partial"), textOff, finish(tc.reason), stopMsg)))
			res, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil)
			if res.Text != "partial" || res.FinishReason != tc.finish {
				t.Fatalf("%+v", res)
			}
			var fe *openrouter.FinishError
			if tc.cut == "" && err != nil || tc.cut != "" && (!errors.As(err, &fe) || fe.Reason != tc.cut) {
				t.Fatalf("err %v", err)
			}
		})
	}
	t.Run("tool_use without a call", func(t *testing.T) {
		c, _ := server(t, stream(events(start, finish("tool_use"), stopMsg)))
		if _, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil); err == nil {
			t.Fatal("no error")
		}
	})
}

func TestErrorsKeepTheirStatusAndText(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reply  func(http.ResponseWriter)
		status int
		msg    string
	}{
		{"overloaded", status(529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), 529, "Overloaded"},
		{"bad key", status(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`), 401, "invalid x-api-key"},
		{"error event", stream(events(start, textOn, text("par"), `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)), 0, "Overloaded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := server(t, tc.reply)
			_, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil)
			var apiErr *openrouter.APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.Message != tc.msg {
				t.Fatalf("err %#v", err)
			}
		})
	}
}

func TestStreamsThatStopEarlyAreIncomplete(t *testing.T) {
	c, _ := server(t, stream(events(start, textOn, text("half"))), stream(events(start, finish("end_turn"))))
	res, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil)
	if !errors.Is(err, openrouter.ErrIncomplete) || res.Text != "half" {
		t.Fatalf("%+v %v", res, err)
	}
	// A stop reason without message_stop is still cut short.
	if _, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil); !errors.Is(err, openrouter.ErrIncomplete) {
		t.Fatalf("err %v", err)
	}
}

func TestCancelKeepsThePartialAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := server(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, events(start, textOn, text("partial")))
		w.(http.Flusher).Flush()
		<-ctx.Done()
	})
	res, err := c.Stream(ctx, openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, func(string) { cancel() })
	if !errors.Is(err, context.Canceled) || res.Text != "partial" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestThinkingBoundElsewhereIsStrippedAndRetriedOnce(t *testing.T) {
	bound := `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block. The block is bound to a different conversation. Remove the block."}}`
	history := []openrouter.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "Let me look.", ToolCalls: []openrouter.ToolCall{{ID: "toolu_1", Type: "function", Function: openrouter.FunctionCall{Name: "demo", Arguments: "{}"}}},
			ReasoningDetails: []json.RawMessage{
				json.RawMessage(`{"type":"thinking","thinking":"","signature":"sig"}`),
				json.RawMessage(`{"type":"text","text":"Let me look."}`),
				json.RawMessage(`{"type":"tool_use","id":"toolu_1","name":"demo","input":{}}`),
			}},
		{Role: "tool", ToolCallID: "toolu_1", Content: "fact"},
	}
	req := openrouter.Request{Models: []string{"m"}, Messages: history, Tools: []openrouter.Tool{demoTool()}}

	c, got := server(t, status(400, bound), stream(fixture(t, "text-stream.sse")))
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	requests := got()
	if len(requests) != 2 {
		t.Fatalf("%d requests", len(requests))
	}
	retried := string(requests[1].body["messages"])
	if strings.Contains(retried, `"thinking"`) || !strings.Contains(retried, `"tool_use"`) || !strings.Contains(retried, "Let me look.") {
		t.Fatalf("retry %s", retried)
	}

	// Once only, and never for other errors.
	c, got = server(t, status(400, bound), status(400, bound))
	if _, err := c.Stream(context.Background(), req, nil); err == nil || len(got()) != 2 {
		t.Fatalf("err %v after %d requests", err, len(got()))
	}
	c, got = server(t, status(400, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`))
	if _, err := c.Stream(context.Background(), req, nil); err == nil || len(got()) != 1 {
		t.Fatalf("err %v after %d requests", err, len(got()))
	}
	// Nothing to strip, nothing to retry.
	c, got = server(t, status(400, bound))
	plain := openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}
	if _, err := c.Stream(context.Background(), plain, nil); err == nil || len(got()) != 1 {
		t.Fatalf("err %v after %d requests", err, len(got()))
	}
}

func TestContinuationStateIsBounded(t *testing.T) {
	big := strings.Repeat("x", maxContinuationBytes+1)
	body := events(start,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"`+big+`"}}`,
		`{"type":"content_block_stop","index":0}`, finish("end_turn"), stopMsg)
	c, _ := server(t, stream(body))
	if _, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil); err == nil {
		t.Fatal("unbounded thinking accepted")
	}
}

func TestModelsAreListedWithTheKey(t *testing.T) {
	c, got := server(t, status(200, `{"data":[{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5","max_input_tokens":1000000},{"type":"model","id":"claude-haiku-5-5","display_name":"Claude Haiku 5.5"}],"has_more":false}`))
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []openrouter.ModelInfo{{ID: "claude-opus-5-5", Name: "Claude Opus 5.5", ContextLength: 1_000_000}, {ID: "claude-haiku-5-5", Name: "Claude Haiku 5.5"}}
	if fmt.Sprint(models) != fmt.Sprint(want) {
		t.Fatalf("%+v", models)
	}
	r := got()[0]
	if r.path != "/v1/models?limit=1000" || r.header.Get("x-api-key") != "fake-anthropic-key" || r.header.Get("anthropic-version") != "2026-10-21" {
		t.Fatalf("%s %v", r.path, r.header)
	}

	c, _ = server(t, status(200, `{"data":[`+strings.Repeat(`{"id":"m"},`, maxModelListBytes/10)+`{"id":"m"}]}`))
	if _, err := c.Models(context.Background()); err == nil {
		t.Fatal("an oversized list was read")
	}
}

func TestRedirectsToAnotherOriginAreRefused(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("followed the redirect with the question and key")
	}))
	defer other.Close()
	c, _ := server(t, func(w http.ResponseWriter) {
		w.Header().Set("Location", strings.Replace(other.URL, "127.0.0.1", "localhost", 1)+"/v1/messages")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	if _, err := c.Stream(context.Background(), openrouter.Request{Models: []string{"m"}, Messages: []openrouter.Message{{Role: "user", Content: "q"}}}, nil); err == nil {
		t.Fatal("no error")
	}
}
