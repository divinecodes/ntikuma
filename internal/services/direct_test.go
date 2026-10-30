package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"ntikuma/internal/config"
	"ntikuma/internal/openrouter"
	"ntikuma/internal/store"
)

type directSetup struct {
	chat     *ChatService
	settings *SettingsService
	keys     *memoryKeys
	rec      *recorder
	conn     config.Connection
}

// newDirect makes conn the active provider, with key saved for its address.
func newDirect(t *testing.T, conn config.Connection, key string) *directSetup {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	keys := &memoryKeys{key: "fake-openrouter-key"}
	d := &directSetup{keys: keys, rec: &recorder{done: make(chan any, 1)}, settings: NewSettingsService(cfg, keys)}
	s, err := d.settings.AddConnection(conn)
	if err != nil {
		t.Fatal(err)
	}
	d.conn = s.Connections[0]
	s.Provider = d.conn.ID
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if key != "" {
		if err := d.settings.SetConnectionKey(d.conn.ID, key); err != nil {
			t.Fatal(err)
		}
	}
	d.chat = NewChatService(st, cfg, keys, d.rec)
	t.Cleanup(func() { _ = d.chat.ServiceShutdown() })
	return d
}

type sentRequest struct {
	Path, Authorization string
	Body                map[string]json.RawMessage
}

func recordingServer(t *testing.T, reply func(step int, w http.ResponseWriter)) (*httptest.Server, func() []sentRequest) {
	var mu sync.Mutex
	var sent []sentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		step := len(sent)
		sent = append(sent, sentRequest{r.URL.Path, r.Header.Get("Authorization"), body})
		mu.Unlock()
		reply(step, w)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []sentRequest { mu.Lock(); defer mu.Unlock(); return append([]sentRequest(nil), sent...) }
}

func answer(text string) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) {
		sse(w, `data: {"model":"qwen3","choices":[{"delta":{"content":"`+text+`"},"finish_reason":"stop"}]}`, "data: [DONE]")
	}
}

func sse(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, l := range lines {
		fmt.Fprintf(w, "%s\n\n", l)
	}
}

func TestDirectAnswersUseTheConnectionsAddressKeyAndModels(t *testing.T) {
	srv, sent := recordingServer(t, answer("direct answer"))
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Local", BaseURL: srv.URL + "/v1", DefaultModel: "llama3.3", Fallbacks: []string{"qwen3"}, ContextTokens: 100_000}, "fake-direct-key")

	// Web is OpenRouter's, so a direct question streams without web tools.
	if _, err := d.chat.Send(SendRequest{Text: "question", Model: "qwen3", Web: true}); err != nil {
		t.Fatal(err)
	}
	if done, ok := wait(t, d.rec).(DoneEvent); !ok || done.Model != "qwen3" {
		t.Fatalf("terminal %+v", done)
	}
	got := sent()[0]
	if got.Path != "/v1/chat/completions" || got.Authorization != "Bearer fake-direct-key" || string(got.Body["model"]) != `"qwen3"` || string(got.Body["stream"]) != "true" || got.Body["tools"] != nil {
		t.Fatalf("request %+v", got)
	}

	if _, err := d.chat.Send(SendRequest{Text: "question", Model: "~anthropic/claude-sonnet-latest"}); err == nil || !strings.Contains(err.Error(), "isn't set up for Local") {
		t.Fatalf("stale override: %v", err)
	}

	// A server on this machine runs without a key.
	if err := d.settings.SetConnectionKey(d.conn.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.chat.Send(SendRequest{Text: "question"}); err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
	if last := sent()[1]; last.Authorization != "" || string(last.Body["model"]) != `"llama3.3"` {
		t.Fatalf("keyless request %+v", last)
	}
}

func TestARunningAnswerKeepsItsEndpointWhenSettingsChange(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("notes"), 0600); err != nil {
		t.Fatal(err)
	}
	started, changed := make(chan struct{}), make(chan struct{})
	moved, movedSent := recordingServer(t, answer("wrong server"))
	srv, sent := recordingServer(t, func(step int, w http.ResponseWriter) {
		if step > 0 {
			answer("done")(step, w)
			return
		}
		close(started)
		<-changed
		args, _ := json.Marshal(fileCall("call-1", "list_directory", root).Function.Arguments)
		sse(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"list_directory","arguments":`+string(args)+`}}]},"finish_reason":"tool_calls"}]}`, "data: [DONE]")
	})
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Local", BaseURL: srv.URL + "/v1", DefaultModel: "llama3.3", ContextTokens: 100_000}, "fake-direct-key")
	enableFileTools(t, d.chat, root)

	if _, err := d.chat.Send(SendRequest{Text: "list my notes"}); err != nil {
		t.Fatal(err)
	}
	<-started
	// Mid-answer: replace the key, move the address, switch to OpenRouter
	// and delete the connection with its key.
	if err := d.settings.SetConnectionKey(d.conn.ID, "fake-replaced-key"); err != nil {
		t.Fatal(err)
	}
	s := d.settings.Get()
	s.Connections[0].BaseURL = moved.URL + "/v1"
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	s.Provider = config.ProviderOpenRouter
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if _, err := d.settings.RemoveConnection(d.conn.ID); err != nil {
		t.Fatal(err)
	}
	close(changed)
	if done, ok := wait(t, d.rec).(DoneEvent); !ok || len(done.Activity) != 1 {
		t.Fatalf("terminal %+v", done)
	}
	requests := sent()
	if len(requests) != 2 || requests[1].Authorization != "Bearer fake-direct-key" || len(movedSent()) != 0 {
		t.Fatalf("continuation went elsewhere: %+v, moved %d", requests, len(movedSent()))
	}

	// The next question uses the new settings.
	var next endpoint
	d.chat.newLLM = func(ep endpoint) LLM {
		next = ep
		return &fakeLLM{result: openrouter.Result{Text: "ok"}}
	}
	if _, err := d.chat.Send(SendRequest{Text: "again"}); err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
	if next.connection != nil || next.key != "fake-openrouter-key" {
		t.Fatalf("next question: %+v", next)
	}
}

func TestSmallContextWindowsTrimEveryChat(t *testing.T) {
	var sent []openrouter.Request
	llm := sizedLLM{tokens: 1_000_000, asked: new([][]string), streamFunc: func(_ context.Context, req openrouter.Request, _ func(string)) (openrouter.Result, error) {
		sent = append(sent, req)
		return openrouter.Result{Text: "ok"}, nil
	}}
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Ollama", BaseURL: "http://localhost:11434/v1", DefaultModel: "llama3.3", ContextTokens: 2048}, "")
	d.chat.newLLM = func(endpoint) LLM { return llm }
	chatID := ""
	for i := range 4 {
		res, err := d.chat.Send(SendRequest{ChatID: chatID, Text: fmt.Sprintf("q%d %s", i, strings.Repeat("x", 2_000))})
		if err != nil {
			t.Fatal(err)
		}
		chatID = res.ChatID
		wait(t, d.rec)
	}
	// About 8k characters: far below the 16k OpenRouter threshold, but over
	// half of a 2,048-token window once the system prompt is counted.
	last := sent[len(sent)-1]
	total := len(last.Messages[0].Content) + conversationChars(last.Messages)
	if total > 2048*charsPerToken/2 || !strings.Contains(last.Messages[0].Content, "left out") || !strings.HasPrefix(last.Messages[len(last.Messages)-1].Content, "q3 ") {
		t.Fatalf("sent %d characters in %d messages", total, len(last.Messages))
	}
	if len(*llm.asked) != 0 {
		t.Fatalf("looked up a catalog for a direct provider: %v", *llm.asked)
	}
}

func TestQuestionsTooLongForTheWindowAreRefused(t *testing.T) {
	called := false
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Ollama", BaseURL: "http://localhost:11434/v1", DefaultModel: "llama3.3", ContextTokens: 1024}, "")
	d.chat.newLLM = func(endpoint) LLM {
		return streamFunc(func(context.Context, openrouter.Request, func(string)) (openrouter.Result, error) {
			called = true
			return openrouter.Result{Text: "ok"}, nil
		})
	}
	if _, err := d.chat.Send(SendRequest{Text: strings.Repeat("x", 5_000)}); err != nil {
		t.Fatal(err)
	}
	got, ok := wait(t, d.rec).(ErrorEvent)
	if !ok || !strings.Contains(got.Error, "Ollama's 1024-token context window") || called {
		t.Fatalf("terminal %+v, sent %v", got, called)
	}
}

func TestDirectRemoteProvidersSkipTheCatalog(t *testing.T) {
	asked := new([][]string)
	llm := sizedLLM{tokens: 1_000, asked: asked, streamFunc: func(context.Context, openrouter.Request, func(string)) (openrouter.Result, error) {
		return openrouter.Result{Text: "ok"}, nil
	}}
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "OpenAI", BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-5.1"}, "fake-openai-key")
	d.chat.newLLM = func(endpoint) LLM { return llm }
	if _, err := d.chat.Send(SendRequest{Text: strings.Repeat("x", 20_000)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := wait(t, d.rec).(DoneEvent); !ok || len(*asked) != 0 {
		t.Fatalf("asked OpenRouter's catalog about %v", *asked)
	}
}

func TestErrorsNameTheProvider(t *testing.T) {
	openRouter := endpoint{name: "OpenRouter"}
	ollama := endpoint{name: "Ollama", connection: &config.Connection{BaseURL: "http://localhost:11434/v1"}}
	openai := endpoint{name: "OpenAI", connection: &config.Connection{BaseURL: "https://api.openai.com/v1"}}
	for _, c := range []struct {
		ep   endpoint
		err  error
		want string
	}{
		{openRouter, &openrouter.APIError{Status: 401}, "OpenRouter rejected the key"},
		{openai, &openrouter.APIError{Status: 401}, "OpenAI rejected the key"},
		{openai, &openrouter.APIError{Status: 402}, "Out of OpenAI credits"},
		{openai, context.DeadlineExceeded, "OpenAI took too long"},
		{ollama, refused(), "Couldn't reach Ollama at http://localhost:11434. Is it running?"},
		{openai, refused(), "connection refused"},
	} {
		if got := c.ep.friendlyError(c.err); !strings.Contains(got, c.want) {
			t.Errorf("%s %v: %q", c.ep.name, c.err, got)
		}
	}
}

func refused() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
}

func TestProviderStatusTellsTheFrontendWhatSendingNeeds(t *testing.T) {
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Ollama", BaseURL: "http://localhost:11434/v1", DefaultModel: "llama3.3", ContextTokens: 4096}, "")
	status, err := d.settings.ProviderStatus()
	if err != nil || status != (ProviderStatus{ID: d.conn.ID, Name: "Ollama", NeedsKey: false, HasKey: false, Web: false}) {
		t.Fatalf("Ollama: %+v %v", status, err)
	}
	s := d.settings.Get()
	s.Connections[0].BaseURL = "https://ollama.example.com/v1"
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if status, _ := d.settings.ProviderStatus(); !status.NeedsKey || status.HasKey {
		t.Fatalf("remote without key: %+v", status)
	}
	if err := d.settings.SetConnectionKey(d.conn.ID, "fake-test-key"); err != nil {
		t.Fatal(err)
	}
	if status, _ := d.settings.ProviderStatus(); !status.HasKey {
		t.Fatalf("remote with key: %+v", status)
	}
	s.Provider = ""
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if status, _ := d.settings.ProviderStatus(); status != (ProviderStatus{ID: config.ProviderOpenRouter, Name: "OpenRouter", NeedsKey: true, HasKey: true, Web: true}) {
		t.Fatalf("OpenRouter: %+v", status)
	}
	raw, _ := json.Marshal(status)
	for _, field := range []string{`"id"`, `"name"`, `"needsKey"`, `"hasKey"`, `"web"`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("frontend can't read %s in %s", field, raw)
		}
	}
}
