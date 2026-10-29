package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"ntikuma/internal/config"
	"ntikuma/internal/openrouter"
	"ntikuma/internal/store"
)

func screenshotAttachment(t *testing.T) Attachment {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 8, 6))); err != nil {
		t.Fatal(err)
	}
	return Attachment{Kind: "screenshot", Label: "Screenshot · 8 × 6", Content: "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())}
}

func TestScreenshotSurvivesSendHistoryAndFollowUp(t *testing.T) {
	llm := &fakeLLM{result: openrouter.Result{Model: "vendor/vision", Text: "A screenshot"}}
	svc, rec, st := newChat(t, llm, "key")
	shot := screenshotAttachment(t)
	res, err := svc.Send(SendRequest{Text: "What is this?", Attachments: []Attachment{shot, {Kind: "clipboard", Label: "Clipboard", Content: "extra context"}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	m := llm.got.Messages[len(llm.got.Messages)-1]
	if len(m.Images) != 1 || m.Images[0] != shot.Content || strings.Contains(m.Content, "base64") || !strings.Contains(m.Content, "extra context") {
		t.Fatalf("screenshot sent as text or lost: %+v", m)
	}
	detail, err := NewHistoryService(st).Get(res.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Messages[0].Attachments) != 2 || detail.Messages[0].Attachments[0].Label != shot.Label || detail.Messages[0].Attachments[0].Content != "" {
		t.Fatalf("history should expose labels only: %+v", detail.Messages[0])
	}
	_, err = svc.Send(SendRequest{ChatID: res.ChatID, Text: "Explain that screenshot"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	if len(llm.got.Messages[1].Images) != 1 || llm.got.Messages[1].Images[0] != shot.Content {
		t.Fatalf("screenshot not replayed on follow-up: %+v", llm.got.Messages)
	}
}

func TestScreenshotValidationBeforeSaving(t *testing.T) {
	svc, _, st := newChat(t, &fakeLLM{}, "key")
	if _, err := svc.Send(SendRequest{Text: "Question", Attachments: []Attachment{{Kind: "screenshot", Content: "file:///tmp/image.png"}}}); err == nil {
		t.Fatal("accepted an invalid screenshot")
	}
	chats, err := st.ListChats(context.Background(), 10)
	if err != nil || len(chats) != 0 {
		t.Fatalf("invalid capture created a chat: %+v, %v", chats, err)
	}
}

func TestSelectedTextRespectsContextLimitAndSurvivesHistory(t *testing.T) {
	llm := &fakeLLM{result: openrouter.Result{Model: "model", Text: "Answer"}}
	svc, rec, st := newChat(t, llm, "key")
	settings := svc.cfg.Get()
	settings.MaxContextChars = 5
	if err := svc.cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Send(SendRequest{Text: "Question", Attachments: []Attachment{{Kind: "selection", Label: "Selection", Content: "ééé more selected text"}}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	content := llm.got.Messages[len(llm.got.Messages)-1].Content
	if !utf8.ValidString(content) || !strings.Contains(content, "éé\n[truncated]") || strings.Contains(content, "more selected text") {
		t.Fatalf("selected text exceeded configured limit: %q", content)
	}
	detail, err := NewHistoryService(st).Get(res.ChatID)
	if err != nil || detail.Messages[0].Attachments[0].Kind != "selection" || detail.Messages[0].Attachments[0].Content != "" {
		t.Fatalf("selection history: %+v, %v", detail, err)
	}
	if _, err := svc.Send(SendRequest{ChatID: res.ChatID, Text: "Follow-up"}); err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	if !strings.Contains(llm.got.Messages[1].Content, "éé\n[truncated]") {
		t.Fatal("stored selection missing from follow-up")
	}
}

type fakeKeys struct{ key string }

func (f fakeKeys) APIKey() (string, error) { return f.key, nil }
func (f fakeKeys) SetAPIKey(string) error  { return nil }

type recorder struct {
	mu     sync.Mutex
	events []string
	text   strings.Builder
	done   chan any
	tools  []ToolEvent
}

func (r *recorder) Emit(name string, data ...any) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, name)
	if d, ok := data[0].(DeltaEvent); ok {
		r.text.WriteString(d.Text)
	}
	if d, ok := data[0].(ToolEvent); ok {
		r.tools = append(r.tools, d)
	}
	if name == EventChatDone || name == EventChatError {
		r.done <- data[0]
	}
	return true
}

type fakeLLM struct {
	got    openrouter.Request
	result openrouter.Result
	err    error
	chunks []string
}

type streamFunc func(context.Context, openrouter.Request, func(string)) (openrouter.Result, error)

func (f streamFunc) Stream(ctx context.Context, req openrouter.Request, onDelta func(string)) (openrouter.Result, error) {
	return f(ctx, req, onDelta)
}

func (f *fakeLLM) Stream(_ context.Context, req openrouter.Request, onDelta func(string)) (openrouter.Result, error) {
	f.got = req
	for _, c := range f.chunks {
		onDelta(c)
	}
	return f.result, f.err
}

func newChat(t *testing.T, llm *fakeLLM, key string) (*ChatService, *recorder, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg, _ := config.Load(filepath.Join(t.TempDir(), "c.json"))
	rec := &recorder{done: make(chan any, 1)}
	svc := NewChatService(st, cfg, fakeKeys{key}, rec)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	svc.newLLM = func(string) LLM { return llm }
	return svc, rec, st
}

func wait(t *testing.T, rec *recorder) any {
	t.Helper()
	select {
	case v := <-rec.done:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the stream to finish")
		return nil
	}
}

func TestSendStreamsAndSaves(t *testing.T) {
	llm := &fakeLLM{
		chunks: []string{"Use ", "bufio.Scanner."},
		result: openrouter.Result{Model: "openai/gpt-5.1", Text: "Use bufio.Scanner.", Fallback: true},
	}
	svc, rec, st := newChat(t, llm, "k")

	res, err := svc.Send(SendRequest{
		Text:        "What's the cleanest way to stream an OpenRouter response in Go?",
		Attachments: []Attachment{{Kind: "clipboard", Label: "Clipboard · 2 lines", Content: "line1\nline2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	done, ok := wait(t, rec).(DoneEvent)
	if !ok || !done.Fallback || done.Model != "openai/gpt-5.1" {
		t.Fatalf("done event %+v", done)
	}
	if rec.text.String() != "Use bufio.Scanner." {
		t.Fatalf("streamed %q", rec.text.String())
	}
	if res.Title != "What's the cleanest way to stream an OpenRouter response in Go" {
		t.Fatalf("title %q", res.Title)
	}

	// The clipboard went along with the question, wrapped as context.
	last := llm.got.Messages[len(llm.got.Messages)-1]
	if !strings.Contains(last.Content, `<context source="Clipboard · 2 lines">`) || llm.got.SessionID != res.ChatID {
		t.Fatalf("request %+v", llm.got)
	}
	if llm.got.Models[0] != config.Defaults().DefaultModel {
		t.Fatalf("models %v", llm.got.Models)
	}

	_, msgs, err := st.GetChat(context.Background(), res.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[1].ID != res.MessageID || !msgs[1].Fallback {
		t.Fatalf("saved %+v", msgs)
	}
}

func TestSendWithoutKey(t *testing.T) {
	svc, _, _ := newChat(t, &fakeLLM{}, "")
	if _, err := svc.Send(SendRequest{Text: "hi"}); err == nil || !strings.Contains(err.Error(), "Preferences") {
		t.Fatalf("got %v", err)
	}
}

func TestErrorKeepsPartial(t *testing.T) {
	llm := &fakeLLM{
		chunks: []string{"half"},
		result: openrouter.Result{Text: "half"},
		err:    &openrouter.APIError{Status: 429, Message: "rate limited"},
	}
	svc, rec, st := newChat(t, llm, "k")
	res, err := svc.Send(SendRequest{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := wait(t, rec).(ErrorEvent)
	if !ok || !strings.Contains(ev.Error, "rate-limited") {
		t.Fatalf("event %+v", ev)
	}
	_, msgs, _ := st.GetChat(context.Background(), res.ChatID)
	if len(msgs) != 2 || msgs[1].Content != "half" {
		t.Fatalf("partial not saved: %+v", msgs)
	}
}

func TestStopMarksStopped(t *testing.T) {
	llm := &fakeLLM{result: openrouter.Result{Text: "some"}, err: context.Canceled}
	svc, rec, _ := newChat(t, llm, "k")
	if _, err := svc.Send(SendRequest{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	done, ok := wait(t, rec).(DoneEvent)
	if !ok || !done.Stopped {
		t.Fatalf("event %+v", done)
	}
}

func TestStopCancelsStreamAndSavesPartial(t *testing.T) {
	svc, rec, st := newChat(t, &fakeLLM{}, "key")
	started := make(chan struct{})
	svc.newLLM = func(string) LLM {
		return streamFunc(func(ctx context.Context, _ openrouter.Request, onDelta func(string)) (openrouter.Result, error) {
			onDelta("Partial answer")
			close(started)
			<-ctx.Done()
			return openrouter.Result{Text: "Partial answer"}, ctx.Err()
		})
	}
	res, err := svc.Send(SendRequest{Text: "Question"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	svc.Stop(res.ChatID)
	done, ok := wait(t, rec).(DoneEvent)
	if !ok || !done.Stopped {
		t.Fatalf("expected stopped event, got %+v", done)
	}
	if svc.Busy(res.ChatID) {
		t.Fatal("chat is still busy after the stopped event")
	}
	_, messages, err := st.GetChat(context.Background(), res.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Content != "Partial answer" {
		t.Fatalf("partial answer not preserved: %+v", messages)
	}
}

func TestFollowUpKeepsChatAndHistory(t *testing.T) {
	llm := &fakeLLM{result: openrouter.Result{Text: "First answer"}}
	svc, rec, st := newChat(t, llm, "key")
	first, err := svc.Send(SendRequest{Text: "First question"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	second, err := svc.Send(SendRequest{ChatID: first.ChatID, Text: "Follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	if second.ChatID != first.ChatID || llm.got.SessionID != first.ChatID {
		t.Fatal("follow-up started a different chat")
	}
	msgs := llm.got.Messages
	if len(msgs) < 3 || msgs[len(msgs)-2].Content != "First answer" || msgs[len(msgs)-1].Content != "Follow-up" {
		t.Fatalf("follow-up is missing conversation history: %+v", msgs)
	}
	recent, err := NewHistoryService(st).Recent(5)
	if err != nil || len(recent) != 1 || recent[0].ID != first.ChatID {
		t.Fatalf("recent chats: %+v, %v", recent, err)
	}
}

func TestTitleFrom(t *testing.T) {
	long := strings.Repeat("word ", 30)
	if got := titleFrom(long); len([]rune(got)) > 80 || !strings.HasSuffix(got, "...") {
		t.Fatalf("long title %q", got)
	}
	if got := titleFrom("Explain channels?\nmore"); got != "Explain channels" {
		t.Fatalf("got %q", got)
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := "Ɔdɔ" // multi-byte runes
	for n := 0; n <= len(s); n++ {
		if got := truncateUTF8(s, n); !strings.HasPrefix(s, got) || !utf8Valid(got) {
			t.Fatalf("n=%d got %q", n, got)
		}
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestFriendlyError(t *testing.T) {
	if got := friendlyError(&openrouter.APIError{Status: 401}); !strings.Contains(got, "key") {
		t.Fatal(got)
	}
	if got := friendlyError(errors.New("boom")); got != "boom" {
		t.Fatal(got)
	}
}

func TestCutOffAnswersKeepTheirTextWithAReason(t *testing.T) {
	for want, streamErr := range map[string]error{
		"The answer reached the model's output limit and was cut off": &openrouter.FinishError{Reason: "length"},
		"The provider's content filter stopped the answer":            &openrouter.FinishError{Reason: "content_filter"},
		"The connection closed before the answer finished":            fmt.Errorf("openrouter: %w", openrouter.ErrIncomplete),
	} {
		llm := &fakeLLM{chunks: []string{"half"}, result: openrouter.Result{Text: "half"}, err: streamErr}
		svc, rec, st := newChat(t, llm, "k")
		res, err := svc.Send(SendRequest{Text: "hi"})
		if err != nil {
			t.Fatal(err)
		}
		ev, ok := wait(t, rec).(ErrorEvent)
		if !ok || ev.Error != want {
			t.Fatalf("event %+v, want %q", ev, want)
		}
		_, msgs, _ := st.GetChat(context.Background(), res.ChatID)
		if len(msgs) != 2 || msgs[1].Content != "half" || decodeMeta(msgs[1].Meta).Error != want {
			t.Fatalf("cut-off answer not saved with its reason: %+v", msgs)
		}
	}
}
