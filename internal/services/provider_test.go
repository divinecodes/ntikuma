package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"ntikuma/internal/config"
	"ntikuma/internal/openrouter"
)

// messagesOf decodes the messages a recorded request sent.
func messagesOf(t *testing.T, req sentRequest) []map[string]json.RawMessage {
	t.Helper()
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(req.Body["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	return msgs
}

func reasoningSent(msgs []map[string]json.RawMessage) []string {
	var out []string
	for _, m := range msgs {
		if r, ok := m["reasoning_content"]; ok {
			out = append(out, string(r))
		}
	}
	return out
}

func TestDeepSeekReasoningIsReplayedToItsProviderWhenToolsAreSent(t *testing.T) {
	root := t.TempDir()
	srv, sent := recordingServer(t, func(step int, w http.ResponseWriter) {
		switch step {
		case 0:
			args, _ := json.Marshal(fileCall("call-1", "list_directory", root).Function.Arguments)
			sse(w, `data: {"model":"deepseek-v4-flash","choices":[{"delta":{"reasoning_content":"plan","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"list_directory","arguments":`+string(args)+`}}]},"finish_reason":"tool_calls"}]}`, "data: [DONE]")
		case 1:
			sse(w, `data: {"model":"deepseek-v4-flash","choices":[{"delta":{"reasoning_content":"thought one","content":"first"},"finish_reason":"stop"}]}`, "data: [DONE]")
		default:
			answer("later")(step, w)
		}
	})
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "DeepSeek", BaseURL: srv.URL + "/v1", DefaultModel: "deepseek-v4-flash", ContextTokens: 100_000}, "fake-direct-key")
	enableFileTools(t, d.chat, root)

	res, err := d.chat.Send(SendRequest{Text: "list my files"})
	if err != nil {
		t.Fatal(err)
	}
	if done, ok := wait(t, d.rec).(DoneEvent); !ok || len(done.Activity) != 1 {
		t.Fatalf("terminal %+v", done)
	}
	// Within the answer, the tool-call turn goes back with its reasoning.
	if got := reasoningSent(messagesOf(t, sent()[1])); !slices.Equal(got, []string{`"plan"`}) {
		t.Fatalf("tool loop reasoning %v", got)
	}

	// A later question with tools replays the answer's reasoning.
	if _, err := d.chat.Send(SendRequest{ChatID: res.ChatID, Text: "and again"}); err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
	if got := reasoningSent(messagesOf(t, sent()[2])); !slices.Equal(got, []string{`"thought one"`}) {
		t.Fatalf("replayed reasoning %v", got)
	}

	// Without tools DeepSeek ignores it, so it isn't sent.
	s := d.settings.Get()
	s.LocalTools = false
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if _, err := d.chat.Send(SendRequest{ChatID: res.ChatID, Text: "no tools"}); err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
	if got := reasoningSent(messagesOf(t, sent()[3])); len(got) != 0 {
		t.Fatalf("reasoning sent without tools: %v", got)
	}

	// Another provider never gets it.
	other, otherSent := recordingServer(t, answer("other"))
	s, err = d.settings.AddConnection(config.Connection{Kind: config.KindOpenAICompatible, Name: "Other", BaseURL: other.URL + "/v1", DefaultModel: "m", ContextTokens: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	s.Provider, s.LocalTools = s.Connections[1].ID, true
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if _, err := d.chat.Send(SendRequest{ChatID: res.ChatID, Text: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
	if got := reasoningSent(messagesOf(t, otherSent()[0])); len(got) != 0 {
		t.Fatalf("another provider got the reasoning: %v", got)
	}
}

func TestStoredReasoningIsBoundedAndCountedInTheHistory(t *testing.T) {
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "DeepSeek", BaseURL: "http://127.0.0.1:1/v1", DefaultModel: "m", ContextTokens: 1_000_000}, "")
	enableFileTools(t, d.chat, t.TempDir())
	long := strings.Repeat("é", maxStoredReasoning)
	requests := useScript(d.chat, func(_ int, _ context.Context, _ openrouter.Request, _ func(string)) (openrouter.Result, error) {
		return openrouter.Result{Text: "answer", FinishReason: "stop", Reasoning: long}, nil
	})
	res, err := d.chat.Send(SendRequest{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
	if _, err := d.chat.Send(SendRequest{ChatID: res.ChatID, Text: "again"}); err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
	replayed := (*requests)[1].Messages[2]
	if replayed.Role != "assistant" || replayed.Reasoning == "" || len(replayed.Reasoning) > maxStoredReasoning || !utf8.ValidString(replayed.Reasoning) {
		t.Fatalf("replayed %s with %d bytes of reasoning", replayed.Role, len(replayed.Reasoning))
	}

	// Replayed reasoning takes space in the window like the text does.
	msgs := []openrouter.Message{{Role: "system"}, {Role: "user", Content: "a"}, {Role: "assistant", Content: "b", Reasoning: strings.Repeat("x", 1000)}, {Role: "user", Content: "c"}}
	if got := trimHistory(msgs, 500); len(got) != 2 || got[1].Content != "c" {
		t.Fatalf("kept %d messages", len(got))
	}
}

func TestDirectFallbacksAreTriedInOrder(t *testing.T) {
	status := func(code int) error { return &openrouter.APIError{Status: code, Message: "nope"} }
	for _, tc := range []struct {
		name     string
		errs     map[string]error // by model
		streamed bool
		tried    []string
		model    string
		fallback bool
		failed   bool
	}{
		{name: "default answers", tried: []string{"a"}, model: "a"},
		{name: "rate-limited", errs: map[string]error{"a": status(429)}, tried: []string{"a", "b"}, model: "b", fallback: true},
		{name: "missing model", errs: map[string]error{"a": status(404)}, tried: []string{"a", "b"}, model: "b", fallback: true},
		{name: "timeout", errs: map[string]error{"a": status(408)}, tried: []string{"a", "b"}, model: "b", fallback: true},
		{name: "server error", errs: map[string]error{"a": status(500), "b": status(503)}, tried: []string{"a", "b", "c"}, model: "c", fallback: true},
		{name: "overloaded", errs: map[string]error{"a": status(529)}, tried: []string{"a", "b"}, model: "b", fallback: true},
		{name: "all down", errs: map[string]error{"a": status(500), "b": status(500), "c": status(502)}, tried: []string{"a", "b", "c"}, failed: true},
		{name: "bad key", errs: map[string]error{"a": status(401)}, tried: []string{"a"}, failed: true},
		{name: "bad request", errs: map[string]error{"a": status(400)}, tried: []string{"a"}, failed: true},
		{name: "error event mid-stream", errs: map[string]error{"a": &openrouter.APIError{Message: "overloaded"}}, tried: []string{"a"}, failed: true},
		{name: "after text arrived", errs: map[string]error{"a": status(500)}, streamed: true, tried: []string{"a"}, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Local", BaseURL: "http://127.0.0.1:1/v1", DefaultModel: "a", Fallbacks: []string{"b", "c"}, ContextTokens: 100_000}, "")
			var tried []string
			d.chat.newLLM = func(endpoint) LLM {
				return streamFunc(func(_ context.Context, req openrouter.Request, delta func(string)) (openrouter.Result, error) {
					if len(req.Models) != 1 {
						t.Errorf("one request for %v", req.Models)
					}
					model := req.Models[0]
					tried = append(tried, model)
					if tc.streamed {
						delta("partial")
					}
					if err := tc.errs[model]; err != nil {
						return openrouter.Result{}, err
					}
					return openrouter.Result{Model: model, Text: "ok", FinishReason: "stop"}, nil
				})
			}
			if _, err := d.chat.Send(SendRequest{Text: "q"}); err != nil {
				t.Fatal(err)
			}
			terminal := wait(t, d.rec)
			if !slices.Equal(tried, tc.tried) {
				t.Fatalf("tried %v, want %v", tried, tc.tried)
			}
			if tc.failed {
				if _, ok := terminal.(ErrorEvent); !ok {
					t.Fatalf("terminal %+v", terminal)
				}
				return
			}
			if done, ok := terminal.(DoneEvent); !ok || done.Model != tc.model || done.Fallback != tc.fallback {
				t.Fatalf("terminal %+v", terminal)
			}
		})
	}
}

func TestStopNeverFallsThroughToTheNextModel(t *testing.T) {
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Local", BaseURL: "http://127.0.0.1:1/v1", DefaultModel: "a", Fallbacks: []string{"b"}, ContextTokens: 100_000}, "")
	started := make(chan struct{})
	var tried []string
	d.chat.newLLM = func(endpoint) LLM {
		return streamFunc(func(ctx context.Context, req openrouter.Request, _ func(string)) (openrouter.Result, error) {
			tried = append(tried, req.Models[0])
			if req.Models[0] == "a" {
				close(started)
				<-ctx.Done()
				// A cancelled request can surface as a status error too.
				return openrouter.Result{}, fmt.Errorf("%w: %w", &openrouter.APIError{Status: 503}, ctx.Err())
			}
			return openrouter.Result{Model: "b", Text: "ok", FinishReason: "stop"}, nil
		})
	}
	res, err := d.chat.Send(SendRequest{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	d.chat.Stop(res.ChatID)
	if done, ok := wait(t, d.rec).(DoneEvent); !ok || !done.Stopped || !slices.Equal(tried, []string{"a"}) {
		t.Fatalf("terminal %+v, tried %v", done, tried)
	}
}

func TestOpenRouterKeepsItsOwnFallbacks(t *testing.T) {
	llm := &fakeLLM{result: openrouter.Result{Text: "ok"}}
	svc, rec, _ := newChat(t, llm, "fake-openrouter-key")
	if _, err := svc.Send(SendRequest{Text: "q"}); err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	// One request with the whole chain; OpenRouter tries it.
	if want := svc.cfg.Get().Models(); !slices.Equal(llm.got.Models, want) || len(want) < 2 {
		t.Fatalf("sent %v, want %v", llm.got.Models, want)
	}
}

func TestFallbacksKeepTheContextLookup(t *testing.T) {
	var asked [][]string
	var llm LLM = fallbackLLM{sizedLLM{tokens: 32_000, asked: &asked}}
	if got := historyBudget(context.Background(), llm, []string{"a"}); got != 64_000 {
		t.Fatalf("budget %d", got)
	}
	if len(asked) != 1 {
		t.Fatalf("asked %v", asked)
	}
}

func TestRefusalsAreSavedAndNotOfferedToContinue(t *testing.T) {
	llm := &fakeLLM{result: openrouter.Result{Text: "I can't help with that."}, err: &openrouter.FinishError{Reason: "refusal"}}
	svc, rec, st := newChat(t, llm, "fake-openrouter-key")
	res, err := svc.Send(SendRequest{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := wait(t, rec).(ErrorEvent)
	if !ok || ev.Error != "The model declined to answer" || ev.CutOff {
		t.Fatalf("terminal %+v", ev)
	}
	_, msgs, err := st.GetChat(context.Background(), res.ChatID)
	if err != nil || msgs[len(msgs)-1].Content != "I can't help with that." {
		t.Fatalf("saved %+v %v", msgs, err)
	}
}

func TestConnectionModelsAreSuggestedFromTheProvider(t *testing.T) {
	list := `{"object":"list","data":[{"id":"gpt-fixture","object":"model"},{"id":"bad\nid"},{"id":"gpt-fixture"},{"id":""},{"id":"` + strings.Repeat("x", 300) + `"},{"id":"o-fixture"}]}`
	fail := false
	srv, sent := recordingServer(t, func(_ int, w http.ResponseWriter) {
		if fail {
			http.Error(w, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, list)
	})
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Local", BaseURL: srv.URL + "/v1", DefaultModel: "m", ContextTokens: 100_000}, "fake-direct-key")

	models, err := d.settings.ConnectionModels(d.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	// Unusable, repeated and oversized IDs are left out.
	if !slices.Equal(ids, []string{"gpt-fixture", "o-fixture"}) {
		t.Fatalf("ids %q", ids)
	}
	if got := sent(); len(got) != 1 || got[0].Path != "/v1/models" || got[0].Authorization != "Bearer fake-direct-key" {
		t.Fatalf("requests %+v", got)
	}

	// Cached for the connection's address.
	fail = true
	if again, err := d.settings.ConnectionModels(d.conn.ID); err != nil || len(again) != 2 || len(sent()) != 1 {
		t.Fatalf("cache: %v %v, %d requests", again, err, len(sent()))
	}
	s := d.settings.Get()
	// Same server, another origin: the key stays with the old one.
	s.Connections[0].BaseURL = strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/v1"
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if _, err := d.settings.ConnectionModels(d.conn.ID); err == nil || len(sent()) != 2 {
		t.Fatalf("moved address used the cache: %v, %d requests", err, len(sent()))
	}

	// The key saved for another address is never sent.
	fail = false
	if _, err := d.settings.ConnectionModels(d.conn.ID); err != nil {
		t.Fatal(err)
	}
	if got := sent()[2]; got.Authorization != "" {
		t.Fatalf("sent the old address's key: %q", got.Authorization)
	}

	if _, err := d.settings.ConnectionModels("c-000000000000"); err == nil {
		t.Fatal("listed models for an unknown provider")
	}
}

// A missing key is shown beside the key field, so it isn't a failure here:
// Wails would log it as one every time Preferences opens.
func TestConnectionModelsAreEmptyWithoutAKeyItNeeds(t *testing.T) {
	// The address can't resolve, so asking the provider would fail.
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Remote", BaseURL: "https://api.example.invalid/v1", DefaultModel: "m"}, "")
	models, err := d.settings.ConnectionModels(d.conn.ID)
	if err != nil || len(models) != 0 {
		t.Fatalf("models %v, err %v", models, err)
	}
}

func TestAnswersSayWhichDirectProviderAnsweredThem(t *testing.T) {
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Local", BaseURL: "http://127.0.0.1:1/v1", DefaultModel: "llama3.3", ContextTokens: 100_000}, "")
	fail := false
	useScript(d.chat, func(_ int, _ context.Context, _ openrouter.Request, _ func(string)) (openrouter.Result, error) {
		if fail {
			return openrouter.Result{Model: "llama3.3"}, errors.New("broke")
		}
		return openrouter.Result{Model: "llama3.3", Text: "ok", FinishReason: "stop"}, nil
	})
	res, err := d.chat.Send(SendRequest{Text: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if done, ok := wait(t, d.rec).(DoneEvent); !ok || done.Provider != "Local" {
		t.Fatalf("terminal %+v", done)
	}
	fail = true
	if _, err := d.chat.Send(SendRequest{ChatID: res.ChatID, Text: "again"}); err != nil {
		t.Fatal(err)
	}
	if ev, ok := wait(t, d.rec).(ErrorEvent); !ok || ev.Provider != "Local" {
		t.Fatalf("terminal %+v", ev)
	}
	// Renaming the provider later doesn't rewrite where answers went.
	s := d.settings.Get()
	s.Connections[0].Name = "Renamed"
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	detail, err := NewHistoryService(d.chat.store).Get(res.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{detail.Messages[1].Provider, detail.Messages[3].Provider}; !slices.Equal(got, []string{"Local", "Local"}) {
		t.Fatalf("history providers %v", got)
	}
	raw, _ := json.Marshal(detail.Messages[1])
	if !strings.Contains(string(raw), `"provider":"Local"`) {
		t.Fatalf("JSON %s", raw)
	}

	// OpenRouter answers stay as they were.
	llm := &fakeLLM{result: openrouter.Result{Text: "ok"}}
	svc, rec, _ := newChat(t, llm, "fake-openrouter-key")
	if _, err := svc.Send(SendRequest{Text: "q"}); err != nil {
		t.Fatal(err)
	}
	if done, _ := wait(t, rec).(DoneEvent); done.Provider != "" {
		t.Fatalf("OpenRouter answer names %q", done.Provider)
	}
}

func TestCommandApprovalsNameTheAnswersProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newDirect(t, config.Connection{Kind: config.KindOpenAICompatible, Name: "Local", BaseURL: "http://127.0.0.1:1/v1", DefaultModel: "llama3.3", ContextTokens: 100_000}, "")
	s := d.settings.Get()
	s.LocalTools, s.AllowShellCommands, s.ToolRoots = true, true, []string{}
	if err := d.settings.Save(s); err != nil {
		t.Fatal(err)
	}
	events := make(chan ApprovalEvent, 64)
	d.chat.events = approvalRecorder{Emitter: d.rec, approvals: events}
	useScript(d.chat, func(step int, _ context.Context, _ openrouter.Request, _ func(string)) (openrouter.Result, error) {
		if step == 0 {
			return openrouter.Result{FinishReason: "tool_calls", ToolCalls: []openrouter.ToolCall{commandCall("shell-1", "true")}}, nil
		}
		return openrouter.Result{Text: "done"}, nil
	})
	if _, err := d.chat.Send(SendRequest{Text: "run it"}); err != nil {
		t.Fatal(err)
	}
	event := pendingCommand(t, events)
	if event.Provider != "Local" {
		t.Fatalf("approval names %q", event.Provider)
	}
	if err := decide(d.chat, event, false); err != nil {
		t.Fatal(err)
	}
	wait(t, d.rec)
}
