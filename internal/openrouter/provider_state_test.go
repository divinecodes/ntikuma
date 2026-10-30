package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func directClient(body, contentType string, inspect func(*http.Request)) *Client {
	c := toolClient(body, contentType, inspect)
	c.Direct = true
	return c
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// sentMessages decodes the messages of the request a client sent.
func sentMessages(t *testing.T, r *http.Request) []map[string]json.RawMessage {
	t.Helper()
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Messages
}

func TestDeepSeekReasoningGoesBackOnItsToolCalls(t *testing.T) {
	for _, f := range []struct{ file, contentType string }{
		{"deepseek-thinking-tool.sse", "text/event-stream"},
		{"deepseek-thinking-tool.json", "application/json"},
	} {
		t.Run(f.file, func(t *testing.T) {
			req := Request{Models: []string{"deepseek-v4-flash"}, Messages: []Message{{Role: "user", Content: "q"}}, Tools: []Tool{dummyTool()}}
			var deltas strings.Builder
			res, err := directClient(fixture(t, f.file), f.contentType, nil).Stream(context.Background(), req, func(d string) { deltas.WriteString(d) })
			if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Arguments != `{"topic":"tides"}` {
				t.Fatalf("%+v %v", res, err)
			}
			if res.Reasoning != "The user wants a demo fact. I should call get_demo_fact." || deltas.Len() != 0 {
				t.Fatalf("reasoning %q, text deltas %q", res.Reasoning, deltas.String())
			}

			// DeepSeek returns 400 when the tool-call turn comes back without it.
			req.Messages = append(req.Messages,
				Message{Role: "assistant", ToolCalls: res.ToolCalls, Reasoning: res.Reasoning, ReasoningDetails: []json.RawMessage{json.RawMessage(`{"type":"reasoning.text"}`)}},
				Message{Role: "tool", ToolCallID: res.ToolCalls[0].ID, Content: "fact"})
			var sent []map[string]json.RawMessage
			_, err = directClient(fixture(t, "ollama-tool-stream.sse"), "text/event-stream", func(r *http.Request) { sent = sentMessages(t, r) }).Stream(context.Background(), req, nil)
			if err != nil {
				t.Fatal(err)
			}
			assistant := sent[1]
			if string(assistant["reasoning_content"]) != `"The user wants a demo fact. I should call get_demo_fact."` {
				t.Errorf("reasoning_content %s", assistant["reasoning_content"])
			}
			// Direct providers get only the standard field, never OpenRouter's.
			if _, ok := assistant["reasoning"]; ok {
				t.Error("sent reasoning")
			}
			if _, ok := assistant["reasoning_details"]; ok {
				t.Error("sent reasoning_details")
			}
		})
	}
}

func TestDirectReasoningFieldsFollowTheProvidersConvention(t *testing.T) {
	// Once any assistant turn carries reasoning_content, every one does, even
	// if empty: a DeepSeek chat may hold answers from other providers.
	msgs := []Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "a"}, {Role: "assistant", Content: "from OpenRouter"},
		{Role: "user", Content: "b"}, {Role: "assistant", Content: "from DeepSeek", Reasoning: "thought"},
		{Role: "user", Content: "c"},
	}
	var sent []map[string]json.RawMessage
	stop := `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	inspect := func(r *http.Request) { sent = sentMessages(t, r) }
	if _, err := directClient(stop, "text/event-stream", inspect).Stream(context.Background(), Request{Models: []string{"m"}, Messages: msgs}, nil); err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]string{2: `""`, 4: `"thought"`} {
		if got := string(sent[i]["reasoning_content"]); got != want {
			t.Errorf("message %d: reasoning_content %s, want %s", i, got, want)
		}
	}
	for _, i := range []int{0, 1, 3, 5} {
		if _, ok := sent[i]["reasoning_content"]; ok {
			t.Errorf("message %d (%s) got reasoning_content", i, sent[i]["role"])
		}
	}

	// Without any reasoning, no message carries the field.
	plain := []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}
	if _, err := directClient(stop, "text/event-stream", inspect).Stream(context.Background(), Request{Models: []string{"m"}, Messages: plain}, nil); err != nil {
		t.Fatal(err)
	}
	for i, m := range sent {
		if _, ok := m["reasoning_content"]; ok {
			t.Errorf("message %d got reasoning_content without any reasoning", i)
		}
	}
	// The request reused the caller's messages without changing them.
	if msgs[2].Reasoning != "" || msgs[4].Reasoning != "thought" {
		t.Errorf("caller's messages changed: %+v", msgs)
	}
}

func TestOpenRouterModeKeepsItsOwnReasoningFields(t *testing.T) {
	// OpenRouter normalises reasoning; its replies and requests never use
	// reasoning_content, and direct servers' plain reasoning field isn't kept.
	stream := `data: {"choices":[{"delta":{"reasoning_content":"rc","reasoning":"r","content":"ok"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	res, err := toolClient(stream, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"m"}}, nil)
	if err != nil || res.Reasoning != "r" {
		t.Fatalf("OpenRouter reasoning %q %v", res.Reasoning, err)
	}
	res, err = directClient(stream, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"m"}}, nil)
	if err != nil || res.Reasoning != "rc" {
		t.Fatalf("direct reasoning %q %v", res.Reasoning, err)
	}

	var sent []map[string]json.RawMessage
	msgs := []Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b", Reasoning: "r"}}
	if _, err := toolClient(stream, "text/event-stream", func(r *http.Request) { sent = sentMessages(t, r) }).Stream(context.Background(), Request{Models: []string{"m"}, Messages: msgs}, nil); err != nil {
		t.Fatal(err)
	}
	if string(sent[1]["reasoning"]) != `"r"` {
		t.Errorf("reasoning %s", sent[1]["reasoning"])
	}
	if _, ok := sent[1]["reasoning_content"]; ok {
		t.Error("sent reasoning_content to OpenRouter")
	}
}

func TestDirectReasoningIsBounded(t *testing.T) {
	stream := toolStream(t, toolEvent(map[string]any{"reasoning_content": strings.Repeat("x", maxReasoningBytes+1)}, "stop"), "[DONE]")
	if _, err := directClient(stream, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"m"}}, nil); err == nil {
		t.Fatal("unbounded reasoning_content accepted")
	}
}

func TestRefusalsAreTheAnswerAndEndIt(t *testing.T) {
	for _, f := range []struct {
		file, contentType string
		direct            bool
	}{
		{"openai-refusal.sse", "text/event-stream", true},
		{"openai-refusal.json", "application/json", true},
		{"openai-refusal.sse", "text/event-stream", false},
	} {
		t.Run(f.file, func(t *testing.T) {
			c := toolClient(fixture(t, f.file), f.contentType, nil)
			c.Direct = f.direct
			var deltas strings.Builder
			res, err := c.Stream(context.Background(), Request{Models: []string{"gpt-fixture"}}, func(d string) { deltas.WriteString(d) })
			const refusal = "I'm sorry, I can't help with that."
			if res.Text != refusal || deltas.String() != refusal {
				t.Fatalf("text %q, deltas %q", res.Text, deltas.String())
			}
			var finish *FinishError
			if !errors.As(err, &finish) || finish.Reason != "refusal" || err.Error() != "the model declined to answer" {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestOllamaToolCallsAssemble(t *testing.T) {
	res, err := directClient(fixture(t, "ollama-tool-stream.sse"), "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"qwen3"}, Tools: []Tool{dummyTool()}}, nil)
	if err != nil || len(res.ToolCalls) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	call := res.ToolCalls[0]
	if call.ID != "call_fixture" || call.Type != "function" || call.Function.Name != "get_demo_fact" || call.Function.Arguments != `{"topic":"tides"}` {
		t.Fatalf("%+v", call)
	}
}

// unindexed is a streamed tool-call delta without its index, as some
// OpenAI-compatible servers send.
func unindexed(id, name, arguments string) toolCallDelta {
	return toolCallDelta{ID: id, Type: "function", Function: FunctionCall{Name: name, Arguments: arguments}}
}

func TestDirectModeAcceptsUnindexedToolCallsOnlyWhenUnambiguous(t *testing.T) {
	tools := []Tool{dummyTool()}
	stream := func(t *testing.T, chunks ...[]toolCallDelta) string {
		var events []any
		for _, calls := range chunks {
			events = append(events, toolEvent(map[string]any{"tool_calls": calls}, ""))
		}
		return toolStream(t, append(events, toolEvent(map[string]any{}, "tool_calls"), "[DONE]")...)
	}
	run := func(body string, direct bool) (Result, error) {
		c := toolClient(body, "text/event-stream", nil)
		c.Direct = direct
		return c.Stream(context.Background(), Request{Models: []string{"m"}, Tools: tools}, nil)
	}

	t.Run("one call split over chunks", func(t *testing.T) {
		body := stream(t, []toolCallDelta{unindexed("a", "demo", `{"to`)}, []toolCallDelta{unindexed("", "", `pic":"x"}`)})
		res, err := run(body, true)
		if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "a" || res.ToolCalls[0].Function.Arguments != `{"topic":"x"}` {
			t.Fatalf("%+v %v", res, err)
		}
		// OpenRouter streams always carry indices; keep refusing ones that don't.
		if res, err := run(body, false); err == nil || len(res.ToolCalls) != 0 {
			t.Fatalf("OpenRouter mode accepted a missing index: %+v", res)
		}
	})
	t.Run("calls told apart by ID", func(t *testing.T) {
		body := stream(t,
			[]toolCallDelta{unindexed("a", "demo", `{"topic":`)},
			[]toolCallDelta{unindexed("b", "other", `{"n":`)},
			[]toolCallDelta{unindexed("a", "", `"x"}`), unindexed("b", "", `1}`)})
		res, err := run(body, true)
		if err != nil || len(res.ToolCalls) != 2 {
			t.Fatalf("%+v %v", res, err)
		}
		if c := res.ToolCalls[0]; c.ID != "a" || c.Function.Name != "demo" || c.Function.Arguments != `{"topic":"x"}` {
			t.Errorf("first %+v", c)
		}
		if c := res.ToolCalls[1]; c.ID != "b" || c.Function.Name != "other" || c.Function.Arguments != `{"n":1}` {
			t.Errorf("second %+v", c)
		}
	})
	t.Run("an ID-less call gets one ID", func(t *testing.T) {
		body := stream(t, []toolCallDelta{unindexed("", "demo", `{"topic"`)}, []toolCallDelta{unindexed("", "", `:"x"}`)})
		res, err := run(body, true)
		if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Arguments != `{"topic":"x"}` {
			t.Fatalf("%+v %v", res, err)
		}
		if id := res.ToolCalls[0].ID; !strings.HasPrefix(id, "call_") || len(id) != len("call_")+16 {
			t.Errorf("generated ID %q", id)
		}
		again, _ := run(body, true)
		if again.ToolCalls[0].ID == res.ToolCalls[0].ID {
			t.Error("generated IDs repeat between answers")
		}
	})
	t.Run("a call without its type", func(t *testing.T) {
		call := unindexed("a", "demo", `{}`)
		call.Type = ""
		res, err := run(stream(t, []toolCallDelta{call}), true)
		if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Type != "function" {
			t.Fatalf("%+v %v", res, err)
		}
	})
	for name, chunks := range map[string][][]toolCallDelta{
		// Without IDs, a second call's first fragment would join the first call.
		"two ID-less calls":          {{unindexed("", "demo", `{}`)}, {unindexed("", "other", `{}`)}},
		"several ID-less in a chunk": {{unindexed("", "demo", `{}`), unindexed("", "other", `{}`)}},
		"ID-less after two open":     {{unindexed("a", "demo", `{`)}, {unindexed("b", "demo", `{`)}, {unindexed("", "", `}`)}},
		"ID-less beside an ID":       {{unindexed("a", "demo", `{`), unindexed("", "", `}`)}},
		"an index then an ID-less":   {{indexedCall(0, "a", "demo", `{`), indexedCall(1, "b", "demo", `{`)}, {unindexed("", "", `}`)}},
		// Made-up indices could collide with real ones, so a stream can't mix them.
		"an index after none": {{unindexed("a", "demo", `{}`)}, {indexedCall(1, "b", "demo", `{}`)}},
		"none after an index": {{indexedCall(0, "a", "demo", `{}`)}, {unindexed("b", "demo", `{}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := run(stream(t, chunks...), true)
			if err == nil || len(res.ToolCalls) != 0 {
				t.Fatalf("ambiguous calls accepted: %+v", res.ToolCalls)
			}
		})
	}
}

func TestDirectModelListsSendTheKey(t *testing.T) {
	for _, direct := range []bool{true, false} {
		var auth string
		c := New("fake-test-key")
		c.Direct = direct
		c.HTTP = &http.Client{Transport: toolTransport(func(r *http.Request) (*http.Response, error) {
			auth = r.Header.Get("Authorization")
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-fixture","object":"model"}]}`))}, nil
		})}
		models, err := c.Models(context.Background())
		if err != nil || len(models) != 1 || models[0].ID != "gpt-fixture" {
			t.Fatalf("%+v %v", models, err)
		}
		// OpenRouter's catalog is public and has never been sent the key.
		if want := map[bool]string{true: "Bearer fake-test-key", false: ""}[direct]; auth != want {
			t.Errorf("direct %v: Authorization %q", direct, auth)
		}
	}
}

func TestDirectModelListsAreBounded(t *testing.T) {
	c := New("")
	c.Direct = true
	c.HTTP = &http.Client{Transport: toolTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"data":[` + strings.Repeat(`{"id":"m"},`, maxDirectModelListBytes/10) + `{"id":"m"}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	if _, err := c.Models(context.Background()); err == nil {
		t.Fatal("an oversized model list was read")
	}
}
