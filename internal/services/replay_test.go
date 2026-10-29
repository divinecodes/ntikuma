package services

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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
	messages := buildMessages("", shotHistory(t, 10, 10, 10, 10, 10, 10), "question now", current, 20000, time.Now())
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
	messages := buildMessages("", shotHistory(t, big, big), "question now", nil, 20000, time.Now())
	if len(messages[1].Images) != 0 || len(messages[3].Images) != 1 {
		t.Fatalf("byte budget: older %d images, newer %d", len(messages[1].Images), len(messages[3].Images))
	}
	// A screenshot on the current question is always sent, even when large.
	current := []Attachment{{Kind: "screenshot", Content: "data:image/png;base64," + strings.Repeat("B", maxReplayImageBytes)}}
	messages = buildMessages("", shotHistory(t, 10), "question now", current, 20000, time.Now())
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
	messages := buildMessages("", history, "question now", nil, 20000, time.Now())
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
	short := buildMessages("", history[:2], "question now", nil, 20000, time.Now())
	if len(short) != 4 || strings.Contains(short[0].Content, "left out") {
		t.Fatalf("short history trimmed: %d messages", len(short))
	}
	// The current question is never dropped, even when it alone is over budget.
	huge := strings.Repeat("z", maxHistoryChars+1)
	alone := buildMessages("", history, huge, nil, 0, time.Now())
	if len(alone) != 2 || alone[1].Content != huge {
		t.Fatalf("current question dropped: %d messages", len(alone))
	}
}
