package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ntikuma/internal/openrouter"
	"ntikuma/internal/store"
)

func shotHistory(t *testing.T, sizes ...int) []store.Message {
	t.Helper()
	var history []store.Message
	for i, size := range sizes {
		raw, err := json.Marshal([]Attachment{{Kind: "screenshot", Label: "Screenshot", Content: fmt.Sprintf("data:image/png;base64,%d%s", i, strings.Repeat("A", size))}})
		if err != nil {
			t.Fatal(err)
		}
		history = append(history,
			store.Message{Role: "user", Content: fmt.Sprintf("question %d", i), Attachments: string(raw)},
			store.Message{Role: "assistant", Content: fmt.Sprintf("answer %d", i)})
	}
	return history
}

func TestReplayKeepsOnlyTheNewestScreenshots(t *testing.T) {
	current := []Attachment{{Kind: "screenshot", Label: "Screenshot", Content: "data:image/png;base64,current"}}
	messages := buildMessages("", shotHistory(t, 10, 10, 10, 10, 10, 10), "question now", current, 20000, time.Now(), "")
	images := 0
	for _, m := range messages {
		images += len(m.Images)
	}
	if images != maxReplayImages {
		t.Fatalf("sent %d images, want %d", images, maxReplayImages)
	}
	last := messages[len(messages)-1]
	if len(last.Images) != 1 || last.Images[0] != current[0].Content {
		t.Fatalf("current screenshot lost: %+v", last)
	}
	// History is system, then six user/assistant pairs. The three newest
	// earlier screenshots stay; the three oldest become a note.
	for i := range 6 {
		user := messages[1+2*i]
		kept := i >= 3
		if (len(user.Images) == 1) != kept || strings.Contains(user.Content, "screenshot sent earlier") == kept {
			t.Fatalf("question %d: kept=%v images=%d content=%q", i, kept, len(user.Images), user.Content)
		}
	}
}

func TestReplayScreenshotsStayWithinAByteBudget(t *testing.T) {
	big := maxReplayImageBytes/2 + 1
	messages := buildMessages("", shotHistory(t, big, big), "question now", nil, 20000, time.Now(), "")
	if len(messages[1].Images) != 0 || len(messages[3].Images) != 1 {
		t.Fatalf("byte budget: older %d images, newer %d", len(messages[1].Images), len(messages[3].Images))
	}
	// A screenshot on the current question is always sent, even when large.
	current := []Attachment{{Kind: "screenshot", Content: "data:image/png;base64," + strings.Repeat("B", maxReplayImageBytes)}}
	messages = buildMessages("", shotHistory(t, 10), "question now", current, 20000, time.Now(), "")
	if len(messages[len(messages)-1].Images) != 1 || len(messages[1].Images) != 0 {
		t.Fatalf("current screenshot dropped or budget ignored: %+v", messages[1])
	}
}

func TestLongHistoryDropsTheOldestTurnsFirst(t *testing.T) {
	var history []store.Message
	for i := range 10 {
		history = append(history,
			store.Message{Role: "user", Content: fmt.Sprintf("q%d %s", i, strings.Repeat("x", 30_000))},
			store.Message{Role: "assistant", Content: fmt.Sprintf("a%d %s", i, strings.Repeat("y", 30_000))})
	}
	messages := trimHistory(buildMessages("", history, "question now", nil, 20000, time.Now(), ""), maxHistoryChars)
	total := 0
	for _, m := range messages[1:] {
		total += len(m.Content)
	}
	if total > maxHistoryChars {
		t.Fatalf("sent %d characters of conversation, budget %d", total, maxHistoryChars)
	}
	if messages[1].Role != "user" || !strings.HasPrefix(messages[len(messages)-2].Content, "a9 ") || messages[len(messages)-1].Content != "question now" {
		t.Fatalf("kept the wrong turns: first %q", messages[1].Content[:4])
	}
	if strings.HasPrefix(messages[1].Content, "q0 ") || !strings.Contains(messages[0].Content, "Older messages in this chat were left out") {
		t.Fatal("oldest turns kept or the model wasn't told")
	}
	short := trimHistory(buildMessages("", history[:2], "question now", nil, 20000, time.Now(), ""), maxHistoryChars)
	if len(short) != 4 || strings.Contains(short[0].Content, "left out") {
		t.Fatalf("short history trimmed: %d messages", len(short))
	}
	// The current question is never dropped, even when it alone is over budget.
	huge := strings.Repeat("z", maxHistoryChars+1)
	alone := trimHistory(buildMessages("", history, huge, nil, 0, time.Now(), ""), maxHistoryChars)
	if len(alone) != 2 || alone[1].Content != huge {
		t.Fatalf("current question dropped: %d messages", len(alone))
	}
}

func TestEmptyAnswersAreReplayedAsAPlaceholder(t *testing.T) {
	history := []store.Message{
		{Role: "user", Content: "first"}, {Role: "assistant", Content: ""},
		{Role: "user", Content: "second"}, {Role: "assistant", Content: " \n"},
		{Role: "user", Content: "third"}, {Role: "assistant", Content: "A real answer"},
	}
	messages := buildMessages("", history, "fourth", nil, 20000, time.Now(), "")
	roles := ""
	for _, m := range messages[1:] {
		roles += m.Role[:1]
		if strings.TrimSpace(m.Content) == "" {
			t.Fatalf("empty %s message replayed", m.Role)
		}
	}
	if roles != "uauauau" || messages[2].Content != noAnswerText || messages[4].Content != noAnswerText || messages[6].Content != "A real answer" {
		t.Fatalf("roles %s: %+v", roles, messages)
	}
}

func TestAFailedAnswerDoesNotBreakTheNextQuestion(t *testing.T) {
	llm := &fakeLLM{err: errors.New("out of credits")}
	svc, rec, st := newChat(t, llm, "k")
	res, err := svc.Send(SendRequest{Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	_, stored, _ := st.GetChat(context.Background(), res.ChatID)
	if len(stored) != 2 || stored[1].Role != "assistant" || stored[1].Content != "" {
		t.Fatalf("expected the failed answer stored empty: %+v", stored)
	}
	llm.err, llm.result = nil, openrouter.Result{Text: "ok"}
	if _, err := svc.Send(SendRequest{ChatID: res.ChatID, Text: "second"}); err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	for _, m := range llm.got.Messages {
		if strings.TrimSpace(m.Content) == "" {
			t.Fatalf("empty %s message sent: %+v", m.Role, llm.got.Messages)
		}
	}
}

type sizedLLM struct {
	streamFunc
	tokens int
	err    error
	asked  *[][]string
}

func (s sizedLLM) ContextLength(_ context.Context, models []string) (int, error) {
	*s.asked = append(*s.asked, models)
	return s.tokens, s.err
}

func TestHistoryBudgetFollowsTheSmallestContextWindow(t *testing.T) {
	var asked [][]string
	sized := func(tokens int, err error) LLM { return sizedLLM{tokens: tokens, err: err, asked: &asked} }
	for _, tc := range []struct {
		name string
		llm  LLM
		want int
	}{
		{"small window", sized(32_000, nil), 64_000},
		{"large window keeps the cap", sized(1_000_000, nil), maxHistoryChars},
		{"unknown models", sized(0, nil), maxHistoryChars},
		{"catalog error", sized(16_000, errors.New("offline")), maxHistoryChars},
		{"no catalog", streamFunc(nil), maxHistoryChars},
	} {
		if got := historyBudget(context.Background(), tc.llm, []string{"vendor/a", "vendor/b"}); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	if len(asked) != 4 || len(asked[0]) != 2 || asked[0][1] != "vendor/b" {
		t.Fatalf("asked for %v", asked)
	}
}

func TestLongChatsFitTheModelsContextWindow(t *testing.T) {
	var asked [][]string
	var sent []openrouter.Request
	llm := sizedLLM{tokens: 16_000, asked: &asked, streamFunc: func(_ context.Context, req openrouter.Request, _ func(string)) (openrouter.Result, error) {
		sent = append(sent, req)
		return openrouter.Result{Text: "ok"}, nil
	}}
	svc, rec, _ := newChat(t, &fakeLLM{}, "key")
	svc.newLLM = func(endpoint) LLM { return llm }
	first, err := svc.Send(SendRequest{Text: "short question", Model: "vendor/small"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	// A short chat is far below any budget, so no catalog lookup delays it.
	if len(asked) != 0 {
		t.Fatalf("looked up %v for a short chat", asked)
	}
	for i := range 4 {
		if _, err := svc.Send(SendRequest{ChatID: first.ChatID, Text: fmt.Sprintf("q%d %s", i, strings.Repeat("x", 12_000)), Model: "vendor/small"}); err != nil {
			t.Fatal(err)
		}
		wait(t, rec)
	}
	last := sent[len(sent)-1]
	total := 0
	for _, m := range last.Messages[1:] {
		total += len(m.Content)
	}
	if total > 32_000 || !strings.HasPrefix(last.Messages[len(last.Messages)-1].Content, "q3 ") || !strings.Contains(last.Messages[0].Content, "left out") {
		t.Fatalf("sent %d characters in %d messages", total, len(last.Messages))
	}
	if len(asked) == 0 || asked[len(asked)-1][0] != "vendor/small" {
		t.Fatalf("asked for %v", asked)
	}
}
