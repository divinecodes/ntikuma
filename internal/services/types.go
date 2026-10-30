package services

import (
	"context"
	"time"

	"ntikuma/internal/openrouter"
)

// Event names emitted to the frontend
const (
	EventChatDelta    = "chat:delta"
	EventChatDone     = "chat:done"
	EventChatError    = "chat:error"
	EventChatTool     = "chat:tool"
	EventChatApproval = "chat:approval"
	EventVoiceLevel   = "voice:level"
)

type VoiceLevelEvent struct {
	RecordingID string  `json:"recordingId"`
	Level       float64 `json:"level"`
	Available   bool    `json:"available"`
}

// Emitter send events to the frontend
type Emitter interface {
	Emit(name string, data ...any) bool
}

// Clipboard reads the clipboard. *application.ClipboardManager satisfies it.
// On Wayland this only works while Ntikuma's window has focus.
type Clipboard interface {
	Text() (string, bool)
}

// secretMarker is a clipboard that can tell when a password manager marked the
// copied data as secret. platform.Clipboard satisfies it.
type secretMarker interface {
	MarkedSecret() bool
}

// KeyStore holds the OpenRouter key and direct connections' keys, each bound
// to the origin it was saved for. secrets.Keyring satisfies it.
type KeyStore interface {
	APIKey() (string, error)
	SetAPIKey(string) error
	ConnectionKey(id, origin string) (string, error)
	SetConnectionKey(id, origin, key string) error
	DeleteConnectionKey(id string) error
}

type LLM interface {
	Stream(ctx context.Context, req openrouter.Request, onDelta func(string)) (openrouter.Result, error)
}

type Attachment struct {
	Kind    string `json:"kind"` // clipboard, selection or screenshot
	Label   string `json:"label"`
	Content string `json:"content"` // Text, or a PNG/JPEG data URL for screenshots.
}

type SendRequest struct {
	// chatid continues a chat. empty starts a new one
	ChatID      string       `json:"chatId"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
	//model overrides the default for this message
	Model string `json:"model"`
	Web   bool   `json:"web"`
}

// SendResult tells the frontend where the answer will stream to.
type SendResult struct {
	ChatID    string `json:"chatId"`
	Title     string `json:"title"`
	MessageID string `json:"messageId"`
}

// DeltaEvent carries streamed text.
type DeltaEvent struct {
	ChatID    string `json:"chatId"`
	MessageID string `json:"messageId"`
	Text      string `json:"text"`
}

// DoneEvent marks the end of an answer.
type DoneEvent struct {
	ChatID    string                `json:"chatId"`
	MessageID string                `json:"messageId"`
	Model     string                `json:"model"`
	Fallback  bool                  `json:"fallback"`
	Stopped   bool                  `json:"stopped"`
	CutOff    bool                  `json:"cutOff,omitempty"`
	Citations []openrouter.Citation `json:"citations,omitempty"`
	Searches  *int                  `json:"searches,omitempty"`
	Activity  []ToolActivity        `json:"activity,omitempty"`
	Provider  string                `json:"provider,omitempty"` // a direct provider's name; empty for OpenRouter
}

// ErrorEvent reports a failed answer. Partial text, if any, was already streamed.
type ErrorEvent struct {
	ChatID    string                `json:"chatId"`
	MessageID string                `json:"messageId"`
	Error     string                `json:"error"`
	CutOff    bool                  `json:"cutOff,omitempty"`
	Model     string                `json:"model,omitempty"`
	Fallback  bool                  `json:"fallback,omitempty"`
	Citations []openrouter.Citation `json:"citations,omitempty"`
	Searches  *int                  `json:"searches,omitempty"`
	Activity  []ToolActivity        `json:"activity,omitempty"`
	Provider  string                `json:"provider,omitempty"`
}

// ToolActivity exposes summaries, never file contents or provider reasoning.
type ToolActivity struct {
	CallID  string `json:"callId"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Status  string `json:"status"` // waiting, started, finished, error, stopped, denied
}

type ToolEvent struct {
	ChatID    string       `json:"chatId"`
	MessageID string       `json:"messageId"`
	Activity  ToolActivity `json:"activity"`
}

type ApprovalEvent struct {
	ChatID           string    `json:"chatId"`
	MessageID        string    `json:"messageId"`
	CallID           string    `json:"callId"`
	Command          string    `json:"command"`
	WorkingDirectory string    `json:"workingDirectory"`
	ExpiresAt        time.Time `json:"expiresAt"`
	State            string    `json:"state"`             // pending, closed
	Outcome          string    `json:"outcome,omitempty"` // allowed, denied, expired, canceled, revoked
	Provider         string    `json:"provider"`          // where the command's output goes
}

// ProviderStatus tells the frontend what sending needs from the active
// provider. Keys themselves never go to the frontend.
type ProviderStatus struct {
	ID       string `json:"id"` // "openrouter" or a connection ID
	Name     string `json:"name"`
	NeedsKey bool   `json:"needsKey"`
	HasKey   bool   `json:"hasKey"`
	Web      bool   `json:"web"` // web search is available
}
