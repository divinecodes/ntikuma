package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ntikuma/internal/openrouter"
)

// The streams below are written by hand from the documented Responses events
// (2026-10-30), not captured from a live response. Encrypted reasoning is a
// placeholder; the API's is opaque and sent back unchanged.

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
	return New(srv.URL+"/v1", "fake-openai-key"), func() []sent { mu.Lock(); defer mu.Unlock(); return append([]sent(nil), got...) }
}

func stream(payloads ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range payloads {
			var head struct{ Type string }
			_ = json.Unmarshal([]byte(p), &head)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, p)
		}
	}
}

func status(code int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}
}

const (
	created   = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","model":"gpt-6.1-sol-2026-09-01","status":"in_progress"}}`
	completed = `{"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","model":"gpt-6.1-sol-2026-09-01","status":"completed"}}`
	reasoning = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"fixture-encrypted-reasoning"}`
	call      = `{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"read_file","arguments":"{\"path\":\"/notes.md\"}","status":"completed"}`
	message   = `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Let me read it.","annotations":[]}]}`
)

func itemDone(index int, item string) string {
	return fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":%d,"output_index":%d,"item":%s}`, index+1, index, item)
}

func textDelta(text string) string {
	raw, _ := json.Marshal(text)
	return `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":` + string(raw) + `}`
}

func question(text string) openrouter.Request {
	return openrouter.Request{Models: []string{"gpt-6.1-sol", "gpt-6.1-terra"}, Messages: []openrouter.Message{
		{Role: "system", Content: "Be brief."},
		{Role: "user", Content: text},
	}}
}

func TestTextStreams(t *testing.T) {
	c, got := server(t, stream(created, textDelta("Hello"), textDelta(" there"), itemDone(0, `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hello there"}]}`), completed))
	var deltas []string
	res, err := c.Stream(context.Background(), question("hi"), func(d string) { deltas = append(deltas, d) })
	if err != nil || res.Text != "Hello there" || res.FinishReason != "stop" || res.Model != "gpt-6.1-sol-2026-09-01" || strings.Join(deltas, "|") != "Hello| there" {
		t.Fatalf("%+v %v %q", res, err, deltas)
	}
	req := got()[0]
	if req.path != "/v1/responses" || req.header.Get("Authorization") != "Bearer fake-openai-key" {
		t.Fatalf("request %s %v", req.path, req.header)
	}
	// One model per request, stateless, no reasoning settings.
	if string(req.body["model"]) != `"gpt-6.1-sol"` || string(req.body["store"]) != "false" || string(req.body["stream"]) != "true" || string(req.body["instructions"]) != `"Be brief."` {
		t.Fatalf("body %s", req.body)
	}
	for _, field := range []string{"reasoning", "reasoning_effort", "max_output_tokens", "tools", "previous_response_id"} {
		if _, ok := req.body[field]; ok {
			t.Fatalf("sent %s: %s", field, req.body[field])
		}
	}
	if string(req.body["input"]) != `[{"role":"user","content":"hi"}]` {
		t.Fatalf("input %s", req.body["input"])
	}
}

func TestRequestsUseTheResponsesFormat(t *testing.T) {
	c, got := server(t, stream(created, completed))
	c.MaxTokens = 30000
	req := openrouter.Request{Models: []string{"gpt-6.1-sol"}, Messages: []openrouter.Message{
		{Role: "system", Content: "Be brief."},
		{Role: "system", Content: "Use tools."},
		{Role: "user", Content: "earlier"},
		{Role: "assistant", Content: "earlier answer"},
		{Role: "assistant", Content: ""},
		{Role: "user", Content: "what's this?", Images: []string{"data:image/png;base64,AAAA"}},
	}, Tools: []openrouter.Tool{
		{Type: openrouter.WebSearchType},
		{Type: "function", Function: &openrouter.Function{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		{Type: "function", Function: &openrouter.Function{Name: "list_tools"}},
	}}
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	body := got()[0].body
	if string(body["instructions"]) != `"Be brief.\n\nUse tools."` || string(body["max_output_tokens"]) != "30000" {
		t.Fatalf("body %s", body)
	}
	want := `[{"role":"user","content":"earlier"},{"role":"assistant","content":"earlier answer"},` +
		`{"role":"user","content":[{"type":"input_text","text":"what's this?"},{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"auto"}]}]`
	if string(body["input"]) != want {
		t.Fatalf("input\n%s\nwant\n%s", body["input"], want)
	}
	// Flat function tools, never OpenRouter's server tools, and not strict.
	wantTools := `[{"type":"function","name":"read_file","description":"Read a file","parameters":{"properties":{"path":{"type":"string"}},"type":"object"},"strict":false},` +
		`{"type":"function","name":"list_tools","parameters":{"type":"object"},"strict":false}]`
	if string(body["tools"]) != wantTools {
		t.Fatalf("tools\n%s\nwant\n%s", body["tools"], wantTools)
	}
}

func TestScreenshotsMustBeDataURLs(t *testing.T) {
	c, got := server(t)
	req := question("look")
	req.Messages[1].Images = []string{"https://example.com/a.png"}
	if _, err := c.Stream(context.Background(), req, nil); err == nil || len(got()) != 0 {
		t.Fatalf("err %v after %d requests", err, len(got()))
	}
}

func TestToolCallsGoBackWithTheirReasoning(t *testing.T) {
	c, got := server(t,
		stream(created, itemDone(0, reasoning), textDelta("Let me read it."), itemDone(1, message), itemDone(2, call), completed),
		stream(created, textDelta("It's a list."), completed))
	req := question("summarise my notes")
	res, err := c.Stream(context.Background(), req, nil)
	if err != nil || res.FinishReason != "tool_calls" || len(res.ToolCalls) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Type != "function" || tc.Function.Name != "read_file" || tc.Function.Arguments != `{"path":"/notes.md"}` {
		t.Fatalf("call %+v", tc)
	}
	if len(res.ReasoningDetails) != 3 {
		t.Fatalf("kept %d items", len(res.ReasoningDetails))
	}

	// The tool loop appends the turn as the tool loop does, then the result.
	req.Messages = append(req.Messages,
		openrouter.Message{Role: "assistant", Content: res.Text, ToolCalls: res.ToolCalls, ReasoningDetails: res.ReasoningDetails},
		openrouter.Message{Role: "tool", ToolCallID: "call_abc", Content: "- milk\n- eggs"})
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	want := `[{"role":"user","content":"summarise my notes"},` + reasoning + `,` + message + `,` + call + `,` +
		`{"type":"function_call_output","call_id":"call_abc","output":"- milk\n- eggs"}]`
	if input := string(got()[1].body["input"]); input != want {
		t.Fatalf("continuation\n%s\nwant\n%s", input, want)
	}
}

func TestToolCallsFromAnotherClientAreRebuilt(t *testing.T) {
	c, got := server(t, stream(created, completed))
	req := question("q")
	req.Messages = append(req.Messages,
		openrouter.Message{Role: "assistant", ToolCalls: []openrouter.ToolCall{{ID: "call_1", Type: "function", Function: openrouter.FunctionCall{Name: "list_directory", Arguments: `{"path":"/"}`}}}},
		openrouter.Message{Role: "tool", ToolCallID: "call_1", Content: ""})
	if _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	want := `[{"role":"user","content":"q"},{"type":"function_call","call_id":"call_1","name":"list_directory","arguments":"{\"path\":\"/\"}"},{"type":"function_call_output","call_id":"call_1","output":""}]`
	if input := string(got()[0].body["input"]); input != want {
		t.Fatalf("input\n%s\nwant\n%s", input, want)
	}
}

func TestRefusalsAreShownAndEndTheAnswer(t *testing.T) {
	c, _ := server(t, stream(created,
		`{"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"I can't help with that."}`,
		itemDone(0, `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":"I can't help with that."}]}`),
		completed))
	res, err := c.Stream(context.Background(), question("q"), nil)
	var finish *openrouter.FinishError
	if !errors.As(err, &finish) || finish.Reason != "refusal" || res.Text != "I can't help with that." {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestIncompleteResponses(t *testing.T) {
	incomplete := func(reason string) string {
		return `{"type":"response.incomplete","sequence_number":3,"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"` + reason + `"}}}`
	}
	for reason, want := range map[string]string{"max_output_tokens": "length", "content_filter": "content_filter"} {
		c, _ := server(t, stream(created, textDelta("partial"), incomplete(reason)))
		res, err := c.Stream(context.Background(), question("q"), nil)
		var finish *openrouter.FinishError
		if !errors.As(err, &finish) || finish.Reason != want || res.Text != "partial" {
			t.Fatalf("%s: %+v %v", reason, res, err)
		}
	}
	// A tool call cut off by the limit never runs.
	c, _ := server(t, stream(created, itemDone(0, call), incomplete("max_output_tokens")))
	if res, err := c.Stream(context.Background(), question("q"), nil); err == nil || len(res.ToolCalls) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	c, _ = server(t, stream(created, incomplete("max_messages")))
	var apiErr *openrouter.APIError
	if _, err := c.Stream(context.Background(), question("q"), nil); !errors.As(err, &apiErr) || !strings.Contains(apiErr.Message, "max_messages") {
		t.Fatalf("err %v", err)
	}
}

func TestErrorsKeepTheirStatusAndText(t *testing.T) {
	var apiErr *openrouter.APIError
	c, _ := server(t, status(400, `{"error":{"message":"Unsupported parameter: 'temperature'.","type":"invalid_request_error","param":"temperature","code":"unsupported_parameter"}}`))
	if _, err := c.Stream(context.Background(), question("q"), nil); !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Message != "Unsupported parameter: 'temperature'." || apiErr.Code != "unsupported_parameter" {
		t.Fatalf("err %#v", err)
	}
	c, _ = server(t, status(429, `{"error":{"message":"Rate limit reached","type":"requests","code":null}}`))
	if _, err := c.Stream(context.Background(), question("q"), nil); !errors.As(err, &apiErr) || apiErr.Status != 429 || apiErr.Code != "requests" {
		t.Fatalf("err %#v", err)
	}
	c, _ = server(t, stream(created, `{"type":"response.failed","sequence_number":2,"response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"The server had an error."}}}`))
	if _, err := c.Stream(context.Background(), question("q"), nil); !errors.As(err, &apiErr) || apiErr.Code != "server_error" || apiErr.Message != "The server had an error." {
		t.Fatalf("err %#v", err)
	}
	c, _ = server(t, stream(created, `{"type":"error","sequence_number":2,"code":"rate_limit_exceeded","message":"Slow down.","param":null}`))
	if _, err := c.Stream(context.Background(), question("q"), nil); !errors.As(err, &apiErr) || apiErr.Code != "rate_limit_exceeded" || apiErr.Message != "Slow down." {
		t.Fatalf("err %#v", err)
	}
}

func TestStreamsThatStopEarlyAreIncomplete(t *testing.T) {
	c, _ := server(t, stream(created, textDelta("half")))
	res, err := c.Stream(context.Background(), question("q"), nil)
	if !errors.Is(err, openrouter.ErrIncomplete) || res.Text != "half" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestContinuationStateIsBounded(t *testing.T) {
	big := `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"` + strings.Repeat("x", maxContinuationBytes) + `"}`
	c, _ := server(t, stream(created, itemDone(0, big), completed))
	if _, err := c.Stream(context.Background(), question("q"), nil); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err %v", err)
	}
}

func TestRedirectsToAnotherOriginAreRefused(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("followed the redirect with %q", r.Header.Get("Authorization"))
	}))
	t.Cleanup(other.Close)
	c, _ := server(t, func(w http.ResponseWriter) {
		w.Header().Set("Location", strings.Replace(other.URL, "127.0.0.1", "localhost", 1)+"/v1/responses")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	if _, err := c.Stream(context.Background(), question("q"), nil); err == nil {
		t.Fatal("followed a cross-origin redirect")
	}
}
