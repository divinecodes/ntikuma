package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ntikuma/internal/store"
)

func TestHistoryJSONMatchesFrontend(t *testing.T) {
	_, _, st := newChat(t, &fakeLLM{}, "key")
	ctx := context.Background()
	chat, err := st.CreateChat(ctx, "Saved chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessage(ctx, store.Message{
		ChatID: chat.ID, Role: "assistant", Content: "```go\nfmt.Println(1)\n```", Model: "test/model",
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := NewHistoryService(st).Get(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Chat     map[string]any   `json:"chat"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Chat["updatedAt"] == nil || len(payload.Messages) != 1 {
		t.Fatalf("unexpected chat payload: %s", b)
	}
	m := payload.Messages[0]
	if m["content"] != detail.Messages[0].Content || m["model"] != "test/model" || m["role"] != "assistant" {
		t.Fatalf("frontend cannot read message fields: %s", b)
	}
}

func TestHistorySearchPreviewAndDelete(t *testing.T) {
	_, _, st := newChat(t, &fakeLLM{}, "key")
	ctx := context.Background()
	chat, err := st.CreateChat(ctx, "Streaming guide")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateChat(ctx, "Other conversation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessage(ctx, store.Message{
		ChatID: chat.ID, Role: "assistant", Content: "Use bufio.Scanner for streaming.",
	}); err != nil {
		t.Fatal(err)
	}
	history := NewHistoryService(st)
	for _, query := range []string{"Streaming guide", "bufio"} {
		hits, err := history.Search(query)
		if err != nil || len(hits) != 1 || hits[0].Chat.ID != chat.ID {
			t.Fatalf("search %q: %+v, %v", query, hits, err)
		}
		if query == "bufio" && !strings.Contains(hits[0].Snippet, store.MarkStart+"bufio"+store.MarkEnd) {
			t.Fatalf("missing snippet markers: %q", hits[0].Snippet)
		}
	}
	detail, err := history.Get(chat.ID)
	if err != nil || len(detail.Messages) != 1 || detail.Messages[0].Content != "Use bufio.Scanner for streaming." {
		t.Fatalf("preview: %+v, %v", detail, err)
	}
	if err := history.Delete(chat.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := history.Get(chat.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted chat is still available: %v", err)
	}
	hits, err := history.Search("bufio")
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted chat remains in search: %+v, %v", hits, err)
	}
	recent, err := history.Recent(50)
	if err != nil || len(recent) != 1 || recent[0].ID != other.ID {
		t.Fatalf("recent chats after deletion: %+v, %v", recent, err)
	}
}

func TestClearHistoryRemovesChatsMessagesAndSearchResults(t *testing.T) {
	_, _, st := newChat(t, &fakeLLM{}, "key")
	ctx := context.Background()
	for _, title := range []string{"First", "Second"} {
		chat, err := st.CreateChat(ctx, title)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddMessage(ctx, store.Message{ChatID: chat.ID, Role: "user", Content: "searchable text"}); err != nil {
			t.Fatal(err)
		}
	}
	history := NewHistoryService(st)
	if err := history.Clear(); err != nil {
		t.Fatal(err)
	}
	recent, err := history.Recent(50)
	if err != nil || len(recent) != 0 {
		t.Fatalf("chats remain after clear: %+v, %v", recent, err)
	}
	hits, err := history.Search("searchable")
	if err != nil || len(hits) != 0 {
		t.Fatalf("messages remain searchable after clear: %+v, %v", hits, err)
	}
}
