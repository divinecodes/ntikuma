package services

import (
	"context"
	"encoding/json"
	"time"

	"ntikuma/internal/openrouter"
	"ntikuma/internal/store"
)

// HistoryService lists, searches and deleted chats
type HistoryService struct {
	store *store.Store
}

// wires the history service
func NewHistoryService(st *store.Store) *HistoryService {
	return &HistoryService{store: st}
}

// a chat with its messages, ready for the UI
type ChatDetail struct {
	Chat     store.Chat    `json:"chat"`
	Messages []MessageView `json:"messages"`
}

// is a stored message with attachments decoded
type MessageView struct {
	ID          string                `json:"id"`
	Role        string                `json:"role"`
	Content     string                `json:"content"`
	Model       string                `json:"model,omitempty"`
	Fallback    bool                  `json:"fallback,omitempty"`
	Attachments []Attachment          `json:"attachments,omitempty"`
	CreatedAt   time.Time             `json:"createdAt"`
	Citations   []openrouter.Citation `json:"citations,omitempty"`
	Searches    *int                  `json:"searches,omitempty"`
	Activity    []ToolActivity        `json:"activity,omitempty"`
	Stopped     bool                  `json:"stopped,omitempty"`
	Error       string                `json:"error,omitempty"`
	// CutOff marks an answer that stopped early and can be continued.
	CutOff bool `json:"cutOff,omitempty"`
	// Provider names the direct provider that answered; empty for OpenRouter.
	Provider string `json:"provider,omitempty"`
}

// finds chats by title or message text. Matched terms in snippets are
// wrapped in \x02...\x03.
func (h *HistoryService) Search(query string) ([]store.SearchHit, error) {
	return h.store.Search(context.Background(), query, 30)
}

// returns the most recently used chats
func (h *HistoryService) Recent(limit int) ([]store.Chat, error) {
	return h.store.ListChats(context.Background(), limit)
}

// loads one chat
func (h *HistoryService) Get(chatID string) (ChatDetail, error) {
	c, msgs, err := h.store.GetChat(context.Background(), chatID)
	if err != nil {
		return ChatDetail{}, err
	}
	out := ChatDetail{Chat: c, Messages: make([]MessageView, 0, len(msgs))}
	for _, m := range msgs {
		v := MessageView{ID: m.ID, Role: m.Role, Content: m.Content, Model: m.Model, Fallback: m.Fallback, CreatedAt: m.CreatedAt}
		meta := decodeMeta(m.Meta)
		v.Citations, v.Searches = meta.Citations, meta.Searches
		v.Activity, v.Stopped, v.Error, v.CutOff = activityViews(meta.Activity), meta.Stopped, meta.Error, meta.CutOff
		v.Provider = meta.Provider
		if m.Attachments != "" {
			_ = json.Unmarshal([]byte(m.Attachments), &v.Attachments)
			// The UI only needs labels; keep clipboard and image data in storage.
			for i := range v.Attachments {
				v.Attachments[i].Content = ""
			}
		}
		out.Messages = append(out.Messages, v)
	}
	return out, nil
}

// renames a chat's title
func (h *HistoryService) Rename(chatID, title string) error {
	return h.store.RenameChat(context.Background(), chatID, title)
}

// delete removes one chat
func (h *HistoryService) Delete(chatID string) error {
	return h.store.DeleteChat(context.Background(), chatID)
}

// clear removes every chat
func (h *HistoryService) Clear() error {
	return h.store.DeleteAll(context.Background())
}
