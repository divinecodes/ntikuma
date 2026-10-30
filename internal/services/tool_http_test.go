package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"ntikuma/internal/openrouter"
)

// Exercise the actual client and registry together, across both transports,
// without credentials or any remote API calls.
func TestFileToolHTTPContinuationAcrossSSEAndJSON(t *testing.T) {
	for _, web := range []bool{false, true} {
		t.Run(map[bool]string{false: "SSE", true: "JSON"}[web], func(t *testing.T) {
			svc, rec, st := newChat(t, &fakeLLM{}, "synthetic-key")
			root := t.TempDir()
			path := filepath.Join(root, "todo.md")
			if err := os.WriteFile(path, []byte("Write the HTTP integration test."), 0600); err != nil {
				t.Fatal(err)
			}
			enableFileTools(t, svc, root)
			call := fileCall("http-read", "read_file", path)
			var mu sync.Mutex
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				var body struct {
					Stream   bool              `json:"stream"`
					Tools    []openrouter.Tool `json:"tools"`
					Provider struct {
						RequireParameters bool `json:"require_parameters"`
					} `json:"provider"`
					Messages []openrouter.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "bad body", 400)
					return
				}
				// Web fetch is withdrawn after the local read, so the continuation has three tools.
				wantTools := map[bool]int{false: 2, true: 4}[web]
				if web && requests > 0 {
					wantTools = 3
				}
				if !body.Provider.RequireParameters || body.Stream == web || len(body.Tools) != wantTools {
					t.Errorf("bad tool request: %+v", body)
				}
				step := requests
				requests++
				message := map[string]any{"content": "HTTP answer"}
				reason := "stop"
				if step == 0 {
					message = map[string]any{"content": nil, "tool_calls": []openrouter.ToolCall{call}}
					reason = "tool_calls"
				} else {
					last := body.Messages[len(body.Messages)-1]
					previous := body.Messages[len(body.Messages)-2]
					if last.Role != "tool" || last.ToolCallID != call.ID || last.Content != "Write the HTTP integration test." || len(previous.ToolCalls) != 1 || previous.ToolCalls[0] != call {
						t.Errorf("bad continuation: %+v / %+v", previous, last)
					}
				}
				if web {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{"model": "glm/test-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}})
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(delta any, finish any) {
					raw, _ := json.Marshal(map[string]any{"model": "gpt/test-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
					fmt.Fprintf(w, "data: %s\n\n", raw)
				}
				if step == 0 {
					emit(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": call.ID, "type": "function", "function": map[string]any{"name": call.Function.Name, "arguments": ""}}}}, nil)
					args := call.Function.Arguments
					for _, fragment := range []string{args[:3], args[3:9], args[9:]} {
						emit(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": fragment}}}}, nil)
					}
				} else {
					emit(message, nil)
				}
				emit(map[string]any{}, reason)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			svc.newLLM = func(ep endpoint) LLM { client := openrouter.New(ep.key); client.BaseURL = server.URL; return client }
			res, err := svc.Send(SendRequest{Text: "Read my todo", Web: web})
			if err != nil {
				t.Fatal(err)
			}
			done, ok := wait(t, rec).(DoneEvent)
			if !ok || len(done.Activity) != 1 || done.Activity[0].Status != "finished" {
				t.Fatalf("terminal: %+v", done)
			}
			mu.Lock()
			count := requests
			mu.Unlock()
			if count != 2 || rec.text.String() != "HTTP answer" {
				t.Fatalf("requests %d, text %q", count, rec.text.String())
			}
			detail, err := NewHistoryService(st).Get(res.ChatID)
			if err != nil || detail.Messages[1].Activity[0].Summary != "Read todo.md" {
				t.Fatalf("history: %+v %v", detail, err)
			}
		})
	}
}
