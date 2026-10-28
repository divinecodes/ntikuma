package services

import (
	"encoding/json"

	"ntikuma/internal/openrouter"
)

// The store keeps metadata opaque so future tool activity can share this column.
type messageMeta struct {
	Citations []openrouter.Citation `json:"citations,omitempty"`
	Searches  *int                  `json:"searches,omitempty"`
	Activity  []toolExecution       `json:"activity,omitempty"`
	Stopped   bool                  `json:"stopped,omitempty"`
	Error     string                `json:"error,omitempty"`
}

// Calls and bounded outputs stay in metadata, for display rather than replay.
// Public history/events expose only ToolActivity summaries.
type toolExecution struct {
	ToolActivity
	Call   openrouter.ToolCall `json:"call"`
	Output string              `json:"output,omitempty"`
}

func activityViews(records []toolExecution) []ToolActivity {
	if len(records) == 0 {
		return nil
	}
	out := make([]ToolActivity, len(records))
	for i, record := range records {
		out[i] = record.ToolActivity
	}
	return out
}

func decodeMeta(raw string) messageMeta {
	var meta messageMeta
	if raw != "" && json.Unmarshal([]byte(raw), &meta) != nil {
		return messageMeta{}
	}
	return meta
}
