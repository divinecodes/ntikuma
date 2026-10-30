package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ntikuma/internal/config"
)

type openAIRequest struct {
	path, auth string
	body       map[string]json.RawMessage
}

func openAIServer(t *testing.T, reply func(step int, w http.ResponseWriter)) (*httptest.Server, func() []openAIRequest) {
	var mu sync.Mutex
	var got []openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		step := len(got)
		got = append(got, openAIRequest{r.URL.RequestURI(), r.Header.Get("Authorization"), body})
		mu.Unlock()
		reply(step, w)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []openAIRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]openAIRequest(nil), got...)
	}
}

func responseEvents(payloads ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range payloads {
			var head struct{ Type string }
			_ = json.Unmarshal([]byte(p), &head)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, p)
		}
	}
}

func TestOpenAIConnectionsAnswerThroughResponses(t *testing.T) {
	root := t.TempDir()
	created := `{"type":"response.created","response":{"model":"gpt-6.1-sol"}}`
	completed := `{"type":"response.completed","response":{"status":"completed"}}`
	srv, sent := openAIServer(t, func(step int, w http.ResponseWriter) {
		if step == 0 {
			args, _ := json.Marshal(fileCall("call_1", "list_directory", root).Function.Arguments)
			responseEvents(created,
				`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"fixture-encrypted"}}`,
				`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"list_directory","arguments":`+string(args)+`}}`,
				completed)(w)
			return
		}
		responseEvents(created, `{"type":"response.output_text.delta","delta":"Done."}`, completed)(w)
	})
	d := newDirect(t, config.Connection{Kind: config.KindOpenAI, Name: "OpenAI", BaseURL: srv.URL + "/v1", DefaultModel: "gpt-6.1-sol", ContextTokens: 1_000_000}, "fake-openai-key")
	enableFileTools(t, d.chat, root)

	res, err := d.chat.Send(SendRequest{Text: "list my notes"})
	if err != nil {
		t.Fatal(err)
	}
	if done, ok := wait(t, d.rec).(DoneEvent); !ok || done.Model != "gpt-6.1-sol" || done.Provider != "OpenAI" || len(done.Activity) != 1 || done.Activity[0].Status != "finished" {
		t.Fatalf("terminal %+v", done)
	}
	requests := sent()
	if len(requests) != 2 || requests[0].path != "/v1/responses" || requests[0].auth != "Bearer fake-openai-key" {
		t.Fatalf("requests %+v", requests)
	}
	if tools := string(requests[0].body["tools"]); !strings.Contains(tools, `"name":"list_directory"`) || strings.Contains(tools, "openrouter:") {
		t.Fatalf("tools %s", tools)
	}
	// The tool step goes back with its encrypted reasoning, then the result.
	var input []struct {
		Type   string `json:"type"`
		Role   string `json:"role"`
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal(requests[1].body["input"], &input); err != nil {
		t.Fatal(err)
	}
	if len(input) != 4 || input[0].Role != "user" || input[1].Type != "reasoning" || input[2].Type != "function_call" || input[3].Type != "function_call_output" || input[3].CallID != "call_1" {
		t.Fatalf("continuation %s", requests[1].body["input"])
	}
	if !strings.Contains(string(requests[1].body["input"]), "fixture-encrypted") {
		t.Fatalf("reasoning was not replayed: %s", requests[1].body["input"])
	}

	// A later question replays text only, never reasoning.
	if _, err := d.chat.Send(SendRequest{ChatID: res.ChatID, Text: "thanks"}); err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
	if later := string(sent()[2].body["input"]); strings.Contains(later, "reasoning") || !strings.Contains(later, "Done.") {
		t.Fatalf("later question %s", later)
	}
}

func TestOpenAIModelsAreSuggested(t *testing.T) {
	srv, sent := openAIServer(t, func(_ int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-6.1-sol","object":"model"}]}`)
	})
	d := newDirect(t, config.Connection{Kind: config.KindOpenAI, Name: "OpenAI", BaseURL: srv.URL + "/v1", DefaultModel: "gpt-6.1-sol", ContextTokens: 1_000_000}, "fake-openai-key")
	models, err := d.settings.ConnectionModels(d.conn.ID)
	if err != nil || len(models) != 1 || models[0].ID != "gpt-6.1-sol" {
		t.Fatalf("%+v %v", models, err)
	}
	if got := sent()[0]; got.path != "/v1/models" || got.auth != "Bearer fake-openai-key" {
		t.Fatalf("request %+v", got)
	}
}
