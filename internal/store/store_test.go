package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestChatLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	c, err := s.CreateChat(ctx, "Streaming an OpenRouter response in Go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, Message{ChatID: c.ID, Role: "user", Content: "How do I stream SSE?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, Message{ChatID: c.ID, Role: "assistant", Content: "Use bufio.Scanner on the body.", Model: "openai/gpt-5.1", Fallback: true}); err != nil {
		t.Fatal(err)
	}
	got, msgs, err := s.GetChat(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != c.Title || len(msgs) != 2 || msgs[1].Model != "openai/gpt-5.1" || !msgs[1].Fallback {
		t.Fatalf("chat %+v msgs %+v", got, msgs)
	}
	if err := s.DeleteChat(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetChat(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestAddMessageToMissingChatFails(t *testing.T) {
	s := open(t)
	if _, err := s.AddMessage(context.Background(), Message{ChatID: "nope", Role: "user", Content: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSearchPrefixAndSnippet(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a, _ := s.CreateChat(ctx, "Fallback order")
	s.AddMessage(ctx, Message{ChatID: a.ID, Role: "assistant", Content: "OpenRouter tries the models array in order."})
	b, _ := s.CreateChat(ctx, "Unrelated")
	s.AddMessage(ctx, Message{ChatID: b.ID, Role: "user", Content: "Jollof or waakye?"})

	hits, err := s.Search(ctx, "openrou", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Chat.ID != a.ID {
		t.Fatalf("hits %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, MarkStart+"OpenRouter"+MarkEnd) {
		t.Fatalf("snippet %q", hits[0].Snippet)
	}

	// FTS syntax in user input must not blow up.
	if _, err := s.Search(ctx, `"unbalanced AND (`, 10); err != nil {
		t.Fatalf("hostile query: %v", err)
	}
}

func TestSearchMatchesTitles(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	c, _ := s.CreateChat(ctx, "Wails v3 single instance")
	hits, err := s.Search(ctx, "single inst", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Chat.ID != c.ID {
		t.Fatalf("hits %+v", hits)
	}
}

func TestFTSQuery(t *testing.T) {
	cases := map[string]string{
		"openrouter":         `"openrouter"*`,
		"  gpt-5.1 fallback": `"gpt-5.1"* "fallback"*`,
		`a"b OR (c)`:         `"a"* "b"* "OR"* "c"*`,
		"":                   "",
	}
	for in, want := range cases {
		if got := ftsQuery(in); got != want {
			t.Errorf("ftsQuery(%q)=%q want %q", in, got, want)
		}
	}
}
