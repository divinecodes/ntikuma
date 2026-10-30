package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ntikuma/internal/config"
	"ntikuma/internal/openrouter"
)

type anthropicRequest struct {
	path, key, version string
	body               map[string]json.RawMessage
}

func anthropicServer(t *testing.T, reply func(step int, w http.ResponseWriter)) (*httptest.Server, func() []anthropicRequest) {
	var mu sync.Mutex
	var got []anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		step := len(got)
		got = append(got, anthropicRequest{r.URL.RequestURI(), r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"), body})
		mu.Unlock()
		reply(step, w)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []anthropicRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]anthropicRequest(nil), got...)
	}
}

func claudeEvents(payloads ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range payloads {
			var head struct{ Type string }
			_ = json.Unmarshal([]byte(p), &head)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, p)
		}
	}
}

func claudeText(text string) func(http.ResponseWriter) {
	raw, _ := json.Marshal(text)
	return claudeEvents(
		`{"type":"message_start","message":{"model":"claude-opus-5-5"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(raw)+`}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`)
}

func TestAnthropicConnectionsAnswerThroughTheirAdapter(t *testing.T) {
	root := t.TempDir()
	srv, sent := anthropicServer(t, func(step int, w http.ResponseWriter) {
		if step > 0 {
			claudeText("Done.")(w)
			return
		}
		input, _ := json.Marshal(fileCall("toolu_1", "list_directory", root).Function.Arguments)
		claudeEvents(
			`{"type":"message_start","message":{"model":"claude-opus-5-5"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"fixture-signature"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"list_directory","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":`+string(input)+`}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
			`{"type":"message_stop"}`)(w)
	})
	d := newDirect(t, config.Connection{Kind: config.KindAnthropic, Name: "Anthropic", BaseURL: srv.URL, DefaultModel: "claude-opus-5-5", ContextTokens: 1_000_000, MaxTokens: 8000}, "fake-anthropic-key")
	enableFileTools(t, d.chat, root)

	// Web search is OpenRouter's, so the question streams without it.
	res, err := d.chat.Send(SendRequest{Text: "list my notes", Web: true})
	if err != nil {
		t.Fatal(err)
	}
	if done, ok := wait(t, d.rec).(DoneEvent); !ok || done.Model != "claude-opus-5-5" || len(done.Activity) != 1 || done.Activity[0].Status != "finished" {
		t.Fatalf("terminal %+v", done)
	}
	requests := sent()
	if len(requests) != 2 {
		t.Fatalf("%d requests", len(requests))
	}
	first := requests[0]
	if first.path != "/v1/messages" || first.key != "fake-anthropic-key" || first.version != "2026-10-21" || string(first.body["max_tokens"]) != "8000" {
		t.Fatalf("request %+v", first)
	}
	if strings.Contains(string(first.body["tools"]), "openrouter:") || !strings.Contains(string(first.body["tools"]), "list_directory") {
		t.Fatalf("tools %s", first.body["tools"])
	}
	// The tool-call turn goes back with its thinking, unchanged.
	var msgs []struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(requests[1].body["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[1].Role != "assistant" || string(msgs[1].Content[0]) != `{"type":"thinking","thinking":"","signature":"fixture-signature"}` || !strings.Contains(string(msgs[2].Content[0]), `"tool_result"`) {
		t.Fatalf("continuation %s", requests[1].body["messages"])
	}

	// A later question replays text only, never thinking.
	if _, err := d.chat.Send(SendRequest{ChatID: res.ChatID, Text: "thanks"}); err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
	if later := string(sent()[2].body["messages"]); strings.Contains(later, "thinking") || !strings.Contains(later, "Done.") {
		t.Fatalf("later question %s", later)
	}
}

func TestAnthropicFallbacksAndOverloads(t *testing.T) {
	srv, sent := anthropicServer(t, func(step int, w http.ResponseWriter) {
		if step == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(529)
			fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
			return
		}
		claudeText("from the fallback")(w)
	})
	d := newDirect(t, config.Connection{Kind: config.KindAnthropic, Name: "Anthropic", BaseURL: srv.URL, DefaultModel: "claude-opus-5-5", Fallbacks: []string{"claude-sonnet-5-5"}, ContextTokens: 1_000_000}, "fake-anthropic-key")
	if _, err := d.chat.Send(SendRequest{Text: "q"}); err != nil {
		t.Fatal(err)
	}
	if done, ok := wait(t, d.rec).(DoneEvent); !ok || !done.Fallback {
		t.Fatalf("terminal %+v", done)
	}
	if got := sent(); len(got) != 2 || string(got[1].body["model"]) != `"claude-sonnet-5-5"` {
		t.Fatalf("requests %+v", got)
	}
	ep := endpoint{name: "Anthropic"}
	if msg := ep.friendlyError(&openrouter.APIError{Status: 529, Message: "Overloaded"}); msg != "Anthropic is overloaded right now. Try again shortly" {
		t.Fatalf("message %q", msg)
	}
}

func TestAnthropicModelsAreSuggested(t *testing.T) {
	srv, sent := anthropicServer(t, func(_ int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5"}],"has_more":false}`)
	})
	d := newDirect(t, config.Connection{Kind: config.KindAnthropic, Name: "Anthropic", BaseURL: srv.URL, DefaultModel: "claude-opus-5-5", ContextTokens: 1_000_000}, "fake-anthropic-key")
	models, err := d.settings.ConnectionModels(d.conn.ID)
	if err != nil || len(models) != 1 || models[0].ID != "claude-opus-5-5" || models[0].Name != "Claude Opus 5.5" {
		t.Fatalf("%+v %v", models, err)
	}
	if got := sent()[0]; got.path != "/v1/models?limit=1000" || got.key != "fake-anthropic-key" {
		t.Fatalf("request %+v", got)
	}
}

func TestAnthropicThinkingCountsTowardTheWindow(t *testing.T) {
	const window = 4096
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("All work and no play makes a dull answer. ", 800)), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, thinking int) ([]anthropicRequest, any) {
		srv, sent := anthropicServer(t, func(step int, w http.ResponseWriter) {
			if step > 0 {
				claudeText("Done.")(w)
				return
			}
			chunk, _ := json.Marshal(strings.Repeat("Let me read the notes first. ", thinking/29))
			input, _ := json.Marshal(fileCall("toolu_1", "read_file", path).Function.Arguments)
			claudeEvents(
				`{"type":"message_start","message":{"model":"claude-opus-5-5"}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":`+string(chunk)+`}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"fixture-signature"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":`+string(input)+`}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
				`{"type":"message_stop"}`)(w)
		})
		d := newDirect(t, config.Connection{Kind: config.KindAnthropic, Name: "Anthropic", BaseURL: srv.URL, DefaultModel: "claude-opus-5-5", ContextTokens: window}, "fake-anthropic-key")
		enableFileTools(t, d.chat, root)
		if _, err := d.chat.Send(SendRequest{Text: "summarise my notes"}); err != nil {
			t.Fatal(err)
		}
		terminal := wait(t, d.rec)
		return sent(), terminal
	}

	t.Run("the result is shortened around the thinking", func(t *testing.T) {
		requests, terminal := run(t, 8000)
		if _, ok := terminal.(DoneEvent); !ok || len(requests) != 2 {
			t.Fatalf("terminal %+v after %d requests", terminal, len(requests))
		}
		// Measured on the wire, where the thinking goes back with the call.
		// The JSON around each block adds a little to the 3/4 estimate.
		body := requests[1].body
		if chars := len(body["system"]) + len(body["tools"]) + len(body["messages"]); chars > window*charsPerToken*7/8 {
			t.Fatalf("continuation is %d characters", chars)
		}
		if !strings.Contains(string(body["messages"]), "[truncated to fit the context window]") {
			t.Fatal("tool result wasn't shortened")
		}
	})

	t.Run("thinking that fills the window", func(t *testing.T) {
		requests, terminal := run(t, 14_000)
		ev, ok := terminal.(ErrorEvent)
		if !ok || !strings.Contains(ev.Error, "Anthropic's 4096-token context window") || len(requests) != 1 {
			t.Fatalf("terminal %+v after %d requests", terminal, len(requests))
		}
		// Nothing runs whose result couldn't be sent.
		if len(ev.Activity) != 0 {
			t.Fatalf("activity %+v", ev.Activity)
		}
	})
}
