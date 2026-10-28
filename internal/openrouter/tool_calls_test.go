package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

type toolTransport func(*http.Request) (*http.Response, error)

func (f toolTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func dummyTool() Tool {
	return Tool{Type: "function", Function: &Function{Name: "get_demo_fact", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"topic": map[string]any{"type": "string"}},
		"required": []string{"topic"}, "additionalProperties": false,
	}}}
}

func toolClient(body, contentType string, inspect func(*http.Request)) *Client {
	c := New("fixture-key")
	c.HTTP = &http.Client{Transport: toolTransport(func(r *http.Request) (*http.Response, error) {
		if inspect != nil {
			inspect(r)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return c
}

func toolEvent(delta any, finish string) map[string]any {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	return map[string]any{"model": "test/model", "choices": []any{choice}}
}

func toolStream(t *testing.T, events ...any) string {
	t.Helper()
	var body strings.Builder
	for _, event := range events {
		if raw, ok := event.(string); ok {
			fmt.Fprintf(&body, "data: %s\n\n", raw)
			continue
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&body, "data: %s\n\n", raw)
	}
	return body.String()
}

func indexedCall(index int, id, name, arguments string) toolCallDelta {
	return toolCallDelta{Index: &index, ID: id, Type: "function", Function: FunctionCall{Name: name, Arguments: arguments}}
}

func TestCapturedFunctionCallsAndContinuations(t *testing.T) {
	for _, fixture := range []struct {
		file, args                 string
		nonStreaming, final, mixed bool
	}{
		{"function-stream.sse", "phase14-probe", false, false, false},
		{"function-completion.json", "phase14-probe", true, false, false},
		{"mixed-tools-completion.json", "go-release", true, false, true},
		{"function-stream-followup.sse", "", false, true, false},
		{"function-completion-followup.json", "", true, true, false},
		{"mixed-tools-completion-followup.json", "", true, true, true},
	} {
		t.Run(fixture.file, func(t *testing.T) {
			body, err := os.ReadFile("testdata/" + fixture.file)
			if err != nil {
				t.Fatal(err)
			}
			contentType := "text/event-stream"
			if fixture.nonStreaming {
				contentType = "application/json"
			}
			client := toolClient(string(body), contentType, nil)
			req := Request{Models: []string{"~anthropic/claude-sonnet-latest", "openai/gpt-5.1"}, Tools: []Tool{dummyTool()}, NonStreaming: fixture.nonStreaming}
			if fixture.mixed {
				req.Tools = append(WebTools(1, 1), req.Tools...)
			}
			var text strings.Builder
			res, err := client.Stream(context.Background(), req, func(s string) { text.WriteString(s) })
			if err != nil {
				t.Fatal(err)
			}
			if text.String() != res.Text || res.Fallback {
				t.Fatalf("text/fallback changed: %+v", res)
			}
			if fixture.final {
				if res.FinishReason != "stop" || len(res.ToolCalls) != 0 || !strings.Contains(res.Text, "NTIKUMA-DEMO-42") {
					t.Fatalf("bad continuation: %+v", res)
				}
			} else {
				if res.FinishReason != "tool_calls" || len(res.ToolCalls) != 1 {
					t.Fatalf("lost function call: %+v", res)
				}
				call := res.ToolCalls[0]
				var args map[string]string
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					t.Fatal(err)
				}
				if call.ID == "" || call.Type != "function" || call.Function.Name != "get_demo_fact" || args["topic"] != fixture.args {
					t.Fatalf("bad call: %+v", call)
				}
			}
			if fixture.mixed && len(res.ReasoningDetails) != 1 {
				t.Fatal("lost signed continuation state")
			}
			if fixture.mixed && fixture.final && (len(res.Citations) == 0 || res.Searches == nil || *res.Searches != 1) {
				t.Fatalf("mixed continuation lost Web metadata: %+v", res)
			}
		})
	}
}

func TestInterleavedToolCallsAssembleByIndexAndKeepEmptyObjects(t *testing.T) {
	response := toolStream(t,
		toolEvent(map[string]any{"content": "Checking ", "tool_calls": []toolCallDelta{indexedCall(1, "call_b", "second", "{"), indexedCall(0, "call_a", "first", `{"path":`)}}, ""),
		toolEvent(map[string]any{"tool_calls": []toolCallDelta{{Index: new(1), Function: FunctionCall{Arguments: "}"}}, {Index: new(0), Function: FunctionCall{Arguments: `"todo`}}}}, ""),
		toolEvent(map[string]any{"content": "now.", "tool_calls": []toolCallDelta{{Index: new(0), Function: FunctionCall{Arguments: `.md"}`}}}}, "tool_calls"),
		"[DONE]",
	)
	var deltas []string
	res, err := toolClient(response, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"test/model"}, Tools: []Tool{dummyTool()}}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	want := []ToolCall{{ID: "call_a", Type: "function", Function: FunctionCall{Name: "first", Arguments: `{"path":"todo.md"}`}}, {ID: "call_b", Type: "function", Function: FunctionCall{Name: "second", Arguments: "{}"}}}
	if !reflect.DeepEqual(res.ToolCalls, want) || res.Text != "Checking now." || strings.Join(deltas, "|") != "Checking |now." {
		t.Fatalf("wrong assembly: %+v %v", res, deltas)
	}
}

func TestMessagesSerializeToolRepliesWithoutLosingScreenshotsOrState(t *testing.T) {
	state := []json.RawMessage{json.RawMessage(`{"type":"reasoning.encrypted","data":"opaque+/=","index":0,"future":{"a":[1,true]}}`)}
	calls := []ToolCall{{ID: "call_a", Type: "function", Function: FunctionCall{Name: "get_demo_fact", Arguments: "{}"}}}
	input := []Message{
		{Role: "user", Content: "Describe", Images: []string{"data:image/png;base64,cGl4ZWw="}},
		{Role: "assistant", ToolCalls: calls, Reasoning: "transport only", ReasoningDetails: state},
		{Role: "tool", ToolCallID: "call_a", Content: `{"answer":42}`},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	if string(messages[1]["content"]) != "null" || string(messages[2]["tool_call_id"]) != `"call_a"` || string(messages[2]["content"]) != `"{\"answer\":42}"` {
		t.Fatalf("bad tool message: %s", raw)
	}
	var gotCalls []ToolCall
	if err := json.Unmarshal(messages[1]["tool_calls"], &gotCalls); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotCalls, calls) || strings.Contains(string(messages[1]["tool_calls"]), `"index"`) {
		t.Fatalf("tool-call transport index leaked: %s", raw)
	}
	var gotState []json.RawMessage
	json.Unmarshal(messages[1]["reasoning_details"], &gotState)
	if !reflect.DeepEqual(gotState, state) || len(messages[1]["reasoning"]) != 0 {
		t.Fatalf("opaque state changed: %s", raw)
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(messages[0]["content"], &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || string(parts[0]["text"]) != `"Describe"` || string(parts[1]["image_url"]) != `{"url":"data:image/png;base64,cGl4ZWw="}` {
		t.Fatalf("screenshot changed: %s", raw)
	}
	plain, _ := json.Marshal(Message{Role: "assistant", Content: ""})
	if string(plain) != `{"role":"assistant","content":""}` {
		t.Fatalf("ordinary message format changed: %s", plain)
	}
	withText, _ := json.Marshal(Message{Role: "assistant", Content: "Checking", ToolCalls: calls})
	if !strings.Contains(string(withText), `"content":"Checking"`) {
		t.Fatal("lost tool preamble")
	}
	plainState, _ := json.Marshal(Message{Role: "assistant", Content: "", Reasoning: "plain-only fallback"})
	if !strings.Contains(string(plainState), `"reasoning":"plain-only fallback"`) {
		t.Fatal("lost plain-only continuation state")
	}
}

func TestFunctionToolsRequireParameterSupportAcrossModelFallbacks(t *testing.T) {
	for _, mode := range []string{"none", "web", "function", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			req := Request{Models: []string{"preferred/model", "backup/model"}}
			if mode == "web" || mode == "mixed" {
				req.Tools = WebTools(5, 3)
				req.MaxToolCalls = 6
				req.NonStreaming = true
			}
			if mode == "function" || mode == "mixed" {
				req.Tools = append(req.Tools, dummyTool())
			}
			client := toolClient(`{"model":"backup/model","choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`, "application/json", func(r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				provider, exists := body["provider"]
				want := mode == "function" || mode == "mixed"
				if exists != want || (want && string(provider) != `{"require_parameters":true}`) {
					t.Fatalf("bad routing: %s", provider)
				}
				if string(body["models"]) != `["preferred/model","backup/model"]` || string(body["stream"]) != fmt.Sprint(!req.NonStreaming) {
					t.Fatalf("fallback/stream policy changed: %s", body)
				}
				if want && !strings.Contains(string(body["tools"]), `"parameters":{"additionalProperties":false`) {
					t.Fatalf("missing function schema: %s", body["tools"])
				}
				var got []Tool
				json.Unmarshal(body["tools"], &got)
				if len(got) != len(req.Tools) {
					t.Fatal("lost mixed tools")
				}
			})
			res, err := client.Stream(context.Background(), req, nil)
			if err != nil || !res.Fallback {
				t.Fatalf("fallback failed: %+v %v", res, err)
			}
		})
	}
}

func TestReasoningStateStaysOpaqueAndOutOfTextDeltas(t *testing.T) {
	first := json.RawMessage(`{"type":"reasoning.text","text":"part one","signature":null,"index":0,"future":"preserve"}`)
	second := json.RawMessage(`{"type":"reasoning.text","text":"part two","signature":"signed","index":0}`)
	response := toolStream(t,
		toolEvent(map[string]any{"reasoning": "one ", "reasoning_details": []json.RawMessage{first}}, ""),
		toolEvent(map[string]any{"reasoning": "two", "reasoning_details": []json.RawMessage{second}}, ""),
		toolEvent(map[string]any{"tool_calls": []toolCallDelta{indexedCall(0, "call_a", "demo", "{}")}}, "tool_calls"), "[DONE]",
	)
	var text strings.Builder
	res, err := toolClient(response, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"test/model"}, Tools: []Tool{dummyTool()}}, func(s string) { text.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if text.Len() != 0 || res.Text != "" || res.Reasoning != "one two" || !reflect.DeepEqual(res.ReasoningDetails, []json.RawMessage{first, second}) {
		t.Fatalf("reasoning was altered or displayed: %+v", res)
	}
}

func TestCompletedSnapshotsDoNotDuplicateArgumentsOrHideOpaqueState(t *testing.T) {
	call := indexedCall(0, "call_a", "demo", "{}")
	state := json.RawMessage(`{"type":"reasoning.encrypted","data":"opaque","index":0}`)
	response := toolStream(t,
		toolEvent(map[string]any{"reasoning": "plain", "tool_calls": []toolCallDelta{call}}, "tool_calls"),
		map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []toolCallDelta{call}, "reasoning_details": []json.RawMessage{state}}, "finish_reason": "tool_calls"}}}, "[DONE]",
	)
	res, err := toolClient(response, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"test/model"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Arguments != "{}" || !reflect.DeepEqual(res.ReasoningDetails, []json.RawMessage{state}) {
		t.Fatalf("snapshot lost or duplicated state: %+v", res)
	}
}

func TestUnsafeToolResponseEnvelopesDoNotExposeCalls(t *testing.T) {
	good := indexedCall(0, "call_a", "demo", "{}")
	tests := map[string][]any{
		"missing index":        {toolEvent(map[string]any{"tool_calls": []toolCallDelta{{ID: "a", Type: "function", Function: FunctionCall{Name: "demo", Arguments: "{}"}}}}, "tool_calls"), "[DONE]"},
		"negative index":       {toolEvent(map[string]any{"tool_calls": []toolCallDelta{indexedCall(-1, "a", "demo", "{}")}}, "tool_calls"), "[DONE]"},
		"missing ID":           {toolEvent(map[string]any{"tool_calls": []toolCallDelta{indexedCall(0, "", "demo", "{}")}}, "tool_calls"), "[DONE]"},
		"missing name":         {toolEvent(map[string]any{"tool_calls": []toolCallDelta{indexedCall(0, "a", "", "{}")}}, "tool_calls"), "[DONE]"},
		"duplicate ID":         {toolEvent(map[string]any{"tool_calls": []toolCallDelta{good, indexedCall(1, "call_a", "demo", "{}")}}, "tool_calls"), "[DONE]"},
		"conflicting header":   {toolEvent(map[string]any{"tool_calls": []toolCallDelta{good}}, ""), toolEvent(map[string]any{"tool_calls": []toolCallDelta{indexedCall(0, "different", "demo", "")}}, "tool_calls"), "[DONE]"},
		"truncated generation": {toolEvent(map[string]any{"tool_calls": []toolCallDelta{good}}, "length"), "[DONE]"},
		"missing finish":       {toolEvent(map[string]any{"tool_calls": []toolCallDelta{good}}, ""), "[DONE]"},
		"empty calls":          {toolEvent(map[string]any{}, "tool_calls"), "[DONE]"},
		"malformed frame":      {toolEvent(map[string]any{"tool_calls": []toolCallDelta{good}}, ""), `{bad json`, toolEvent(map[string]any{}, "tool_calls"), "[DONE]"},
		"provider error":       {toolEvent(map[string]any{"tool_calls": []toolCallDelta{good}}, "tool_calls"), map[string]any{"error": map[string]any{"message": "provider failed"}}, "[DONE]"},
		"oversized arguments":  {toolEvent(map[string]any{"tool_calls": []toolCallDelta{indexedCall(0, "a", "demo", strings.Repeat("x", maxToolArgumentBytes+1))}}, "tool_calls"), "[DONE]"},
		"invalid UTF-8":        {`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"demo","arguments":"` + string([]byte{0xff}) + `"}}]},"finish_reason":"tool_calls"}]}`, "[DONE]"},
	}
	for name, events := range tests {
		t.Run(name, func(t *testing.T) {
			response := toolStream(t, events...)
			res, err := toolClient(response, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"test/model"}, Tools: []Tool{dummyTool()}}, nil)
			if err == nil || len(res.ToolCalls) != 0 {
				t.Fatalf("unsafe response exposed calls: %+v %v", res, err)
			}
		})
	}
}

func TestToolCallsRequireStreamTerminatorAndHonorCancellation(t *testing.T) {
	response := toolStream(t, toolEvent(map[string]any{"content": "Partial", "tool_calls": []toolCallDelta{indexedCall(0, "a", "demo", "{}")}}, "tool_calls"))
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			res, err := toolClient(response, "text/event-stream", nil).Stream(ctx, Request{Models: []string{"test/model"}}, func(string) {
				if canceled {
					cancel()
				}
			})
			want := error(io.ErrUnexpectedEOF)
			if canceled {
				want = context.Canceled
			}
			if !errors.Is(err, want) || len(res.ToolCalls) != 0 || res.Text != "Partial" {
				t.Fatalf("incomplete/canceled calls escaped: %+v %v", res, err)
			}
		})
	}
	jsonResponse := `{"choices":[{"message":{"content":"Partial","tool_calls":[{"id":"a","type":"function","function":{"name":"demo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := toolClient(jsonResponse, "application/json", nil).Stream(ctx, Request{Models: []string{"test/model"}}, func(string) { cancel() })
	if !errors.Is(err, context.Canceled) || len(res.ToolCalls) != 0 || res.Text != "Partial" {
		t.Fatalf("canceled JSON response exposed calls: %+v %v", res, err)
	}
}

func TestToolArgumentJSONRemainsUntrustedForRegistryErrorRecovery(t *testing.T) {
	response := toolStream(t, toolEvent(map[string]any{"tool_calls": []toolCallDelta{indexedCall(0, "a", "demo", "{bad arguments")}}, "tool_calls"), "[DONE]")
	res, err := toolClient(response, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"test/model"}}, nil)
	if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Arguments != "{bad arguments" {
		t.Fatalf("registry cannot report malformed arguments as tool errors: %+v %v", res, err)
	}
}

func TestToolResponseCollectionHasFiniteBudgets(t *testing.T) {
	for _, budget := range []string{"calls", "total arguments", "reasoning"} {
		t.Run(budget, func(t *testing.T) {
			delta := map[string]any{}
			var calls []toolCallDelta
			switch budget {
			case "calls":
				for i := 0; i <= maxResponseToolCalls; i++ {
					calls = append(calls, indexedCall(i, fmt.Sprint(i), "demo", "{}"))
				}
			case "total arguments":
				for i := 0; i <= maxTotalToolArgumentBytes/maxToolArgumentBytes; i++ {
					calls = append(calls, indexedCall(i, fmt.Sprint(i), "demo", strings.Repeat("x", maxToolArgumentBytes)))
				}
			case "reasoning":
				delta["reasoning"] = strings.Repeat("x", maxReasoningBytes+1)
			}
			delta["tool_calls"] = calls
			response := toolStream(t, toolEvent(delta, "tool_calls"), "[DONE]")
			res, err := toolClient(response, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"test/model"}}, nil)
			if err == nil || len(res.ToolCalls) != 0 {
				t.Fatalf("unbounded response accepted: %+v %v", res, err)
			}
		})
	}
}

func TestCapturedToolContinuationRoundTrip(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			files := []string{"function-completion.json", "function-completion-followup.json"}
			if mixed {
				files = []string{"mixed-tools-completion.json", "mixed-tools-completion-followup.json"}
			}
			client := New("fixture-key")
			requests := 0
			var first Result
			client.HTTP = &http.Client{Transport: toolTransport(func(r *http.Request) (*http.Response, error) {
				var request struct {
					Provider providerOptions `json:"provider"`
					Messages []Message       `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if !request.Provider.RequireParameters {
					t.Fatal("continuation lost provider constraint")
				}
				if requests == 1 {
					if len(request.Messages) != 3 {
						t.Fatalf("missing continuation messages: %+v", request.Messages)
					}
					assistant, reply := request.Messages[1], request.Messages[2]
					wantState, _ := json.Marshal(first.ReasoningDetails)
					gotState, _ := json.Marshal(assistant.ReasoningDetails)
					if assistant.Role != "assistant" || !reflect.DeepEqual(assistant.ToolCalls, first.ToolCalls) || string(gotState) != string(wantState) || (len(first.ReasoningDetails) > 0 && assistant.Reasoning != "") || reply.Role != "tool" || reply.ToolCallID != first.ToolCalls[0].ID || reply.Content != "NTIKUMA-DEMO-42" {
						t.Fatalf("continuation lost call ID, output, or opaque state: %+v", request.Messages)
					}
				}
				if requests >= len(files) {
					t.Fatal("unexpected extra model request")
				}
				body, err := os.ReadFile("testdata/" + files[requests])
				if err != nil {
					t.Fatal(err)
				}
				requests++
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			req := Request{Models: []string{"~anthropic/claude-sonnet-latest"}, Messages: []Message{{Role: "user", Content: "Demo fact"}}, Tools: []Tool{dummyTool()}, NonStreaming: true}
			if mixed {
				req.Tools = append(WebTools(1, 1), req.Tools...)
			}
			var err error
			first, err = client.Stream(context.Background(), req, nil)
			if err != nil || len(first.ToolCalls) != 1 {
				t.Fatalf("first completion: %+v %v", first, err)
			}
			req.Messages = append(req.Messages,
				Message{Role: "assistant", Content: first.Text, ToolCalls: first.ToolCalls, Reasoning: first.Reasoning, ReasoningDetails: first.ReasoningDetails},
				Message{Role: "tool", ToolCallID: first.ToolCalls[0].ID, Content: "NTIKUMA-DEMO-42"},
			)
			last, err := client.Stream(context.Background(), req, nil)
			if err != nil || last.FinishReason != "stop" || !strings.Contains(last.Text, "NTIKUMA-DEMO-42") || requests != 2 {
				t.Fatalf("follow-up failed: %+v %v", last, err)
			}
		})
	}
}

func TestJSONCallsWithoutTransportIndicesAndOtherChoices(t *testing.T) {
	response := `{"choices":[{"index":1,"message":{"content":"ignore","tool_calls":[{"id":"other","type":"function","function":{"name":"other","arguments":"{}"}}]},"finish_reason":"tool_calls"},{"index":0,"message":{"content":null,"tool_calls":[{"id":"a","type":"function","function":{"name":"first","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"second","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	res, err := toolClient(response, "application/json", nil).Stream(context.Background(), Request{Models: []string{"test/model"}}, nil)
	if err != nil || res.Text != "" || len(res.ToolCalls) != 2 || res.ToolCalls[0].ID != "a" || res.ToolCalls[1].ID != "b" {
		t.Fatalf("JSON call order or choice isolation failed: %+v %v", res, err)
	}
}

func TestToolEnvelopeErrorPreservesTextAndWebMetadata(t *testing.T) {
	response := toolStream(t, map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{
			"content": "Partial", "annotations": []any{map[string]any{"type": "url_citation", "url_citation": Citation{URL: "https://go.dev/", Title: "Go"}}},
			"tool_calls": []toolCallDelta{{ID: "missing-index", Type: "function", Function: FunctionCall{Name: "demo"}}},
		}}}, "usage": map[string]any{"server_tool_use": map[string]any{"web_search_requests": 1}},
	}, "[DONE]")
	res, err := toolClient(response, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"test/model"}, Tools: []Tool{dummyTool()}}, nil)
	if err == nil || len(res.ToolCalls) != 0 || res.Text != "Partial" || len(res.Citations) != 1 || res.Searches == nil || *res.Searches != 1 {
		t.Fatalf("error lost partial answer metadata: %+v %v", res, err)
	}
}

func TestConflictingToolSnapshotCannotChangeTheRequestedOperation(t *testing.T) {
	response := toolStream(t,
		toolEvent(map[string]any{"tool_calls": []toolCallDelta{indexedCall(0, "a", "demo", `{"path":"first"}`)}}, "tool_calls"),
		map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []toolCallDelta{indexedCall(0, "a", "demo", `{"path":"different"}`)}}}}}, "[DONE]",
	)
	res, err := toolClient(response, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"test/model"}}, nil)
	if err == nil || len(res.ToolCalls) != 0 {
		t.Fatalf("conflicting snapshot exposed calls: %+v %v", res, err)
	}
}

func TestJSONToolCompletionRejectsInvalidUTF8(t *testing.T) {
	response := `{"choices":[{"message":{"tool_calls":[{"id":"a","type":"function","function":{"name":"demo","arguments":"` + string([]byte{0xff}) + `"}}]},"finish_reason":"tool_calls"}]}`
	res, err := toolClient(response, "application/json", nil).Stream(context.Background(), Request{Models: []string{"test/model"}, Tools: []Tool{dummyTool()}}, nil)
	if err == nil || len(res.ToolCalls) != 0 {
		t.Fatalf("invalid UTF-8 was silently rewritten: %+v %v", res, err)
	}
}

func TestToolArgumentBudgetsAllowTheirExactBoundary(t *testing.T) {
	var calls []toolCallDelta
	arguments := strings.Repeat(" ", maxToolArgumentBytes-2) + "{}"
	for i := 0; i < maxTotalToolArgumentBytes/maxToolArgumentBytes; i++ {
		calls = append(calls, indexedCall(i, fmt.Sprint(i), "demo", arguments))
	}
	response := toolStream(t, toolEvent(map[string]any{"tool_calls": calls}, "tool_calls"), "[DONE]")
	res, err := toolClient(response, "text/event-stream", nil).Stream(context.Background(), Request{Models: []string{"test/model"}}, nil)
	if err != nil || len(res.ToolCalls) != len(calls) {
		t.Fatalf("valid boundary rejected: %d %v", len(res.ToolCalls), err)
	}
	for _, call := range res.ToolCalls {
		if call.Function.Arguments != arguments {
			t.Fatal("arguments were truncated")
		}
	}
}
