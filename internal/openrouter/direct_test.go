package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDirectModeSendsPlainChatCompletions(t *testing.T) {
	var body map[string]json.RawMessage
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		header = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		sse(w, `data: {"model":"llama3.3","choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`, "data: [DONE]")
	}))
	defer srv.Close()

	tools := append(WebTools(5, 3), Tool{Type: "function", Function: &Function{Name: "read_file", Parameters: map[string]any{"type": "object"}}})
	req := Request{Models: []string{"llama3.3", "qwen3"}, Messages: []Message{{Role: "user", Content: "q", Images: []string{"data:image/png;base64,AAAA"}}}, SessionID: "chat", Tools: tools, MaxToolCalls: 6}
	for _, key := range []string{"", "fake-test-key"} {
		c := New(key)
		c.BaseURL, c.Direct = srv.URL+"/v1", true
		res, err := c.Stream(context.Background(), req, nil)
		if err != nil || res.Text != "hi" || res.Fallback {
			t.Fatalf("key %q: %+v %v", key, res, err)
		}
		// OpenAI rejects unknown top-level fields, so only standard ones go.
		for field := range body {
			switch field {
			case "model", "messages", "stream", "tools":
			default:
				t.Errorf("sent OpenRouter field %q", field)
			}
		}
		if string(body["model"]) != `"llama3.3"` {
			t.Errorf("model %s", body["model"])
		}
		var sent []Tool
		_ = json.Unmarshal(body["tools"], &sent)
		if len(sent) != 1 || sent[0].Type != "function" {
			t.Errorf("tools %s", body["tools"])
		}
		if got := header.Get("Authorization"); (key == "" && got != "") || (key != "" && got != "Bearer "+key) {
			t.Errorf("key %q: Authorization %q", key, got)
		}
		if header.Get("X-Title") != "" {
			t.Error("sent OpenRouter's app title")
		}
	}
}

func TestOpenRouterModeStillNeedsAKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("sent to OpenRouter without a key")
	}))
	defer srv.Close()
	c := New("")
	c.BaseURL = srv.URL
	if _, err := c.Stream(context.Background(), Request{Models: []string{"m"}, Messages: []Message{{Role: "user", Content: "q"}}}, nil); err == nil {
		t.Fatal("no error without a key")
	}
}
