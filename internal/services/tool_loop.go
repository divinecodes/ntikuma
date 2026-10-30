package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"ntikuma/internal/config"
	"ntikuma/internal/openrouter"
	"ntikuma/internal/tools"
)

const (
	maxToolSteps         = 8
	maxLocalCalls        = 24
	maxContinuationBytes = 4 * 1024 * 1024
	// minToolResultChars is the smallest shortened tool result worth sending;
	// below it the answer stops instead.
	minToolResultChars = 500
)

const windowNote = "\n[truncated to fit the context window]"

type turnResult struct {
	openrouter.Result
	Activity []toolExecution
}

func toolRegistry(settings config.Settings) (*tools.Registry, error) {
	registry := &tools.Registry{}
	if settings.LocalTools {
		for _, tool := range tools.FileTools(settings.ToolRoots, settings.AllowHiddenFiles) {
			if err := registry.Register(tool); err != nil {
				return nil, err
			}
		}
		if settings.AllowShellCommands {
			if err := registry.Register(tools.ShellTool()); err != nil {
				return nil, err
			}
		}
	}
	return registry, nil
}

// runTurn keeps tool exchanges inside this one answer. Later questions replay
// stored user/assistant text and screenshots, never calls, results or reasoning.
// hiddenAtStart is the hidden-files setting when the answer began. Like local
// tools and shell commands, turning it on takes effect from the next question.
// With a known window, tool results are shortened so each continuation fits
// in three quarters of it; when that can't leave a useful part, it stops.
func (s *ChatService) runTurn(ctx context.Context, llm LLM, req openrouter.Request, limit contextLimit, hiddenAtStart bool, chatID, answerID string, onDelta func(string), flush func()) (all turnResult, err error) {
	var serverTools []openrouter.Tool
	localAtStart := false
	shellAtStart := false
	for _, tool := range req.Tools {
		if tool.Type == "function" {
			localAtStart = true
			if tool.Function != nil && tool.Function.Name == "run_command" {
				shellAtStart = true
			}
		} else {
			serverTools = append(serverTools, tool)
		}
	}
	var text strings.Builder
	seenCitations := make(map[string]bool)
	seenCalls := make(map[string]bool)
	continuationBytes := 0
	// Once a local tool has run, the answer holds private data. Web fetch could
	// send it to a URL a page or file chose, so later steps stop offering it.
	localRan := false
	appendMessage := func(message openrouter.Message) error {
		raw, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if len(raw) > maxContinuationBytes-continuationBytes {
			return errors.New("local tool conversation exceeded the 4 MiB continuation limit; try a narrower request")
		}
		continuationBytes += len(raw)
		req.Messages = append(req.Messages, message)
		return nil
	}
	defer func() { all.Text = text.String() }()
	for step := 0; step < maxToolSteps; step++ {
		if err := ctx.Err(); err != nil {
			return all, err
		}
		// Removing a folder or disabling tools takes effect before another read,
		// including while an answer is running. Enabling tools starts next turn.
		req.Tools = nil
		for _, tool := range serverTools {
			if !(localRan && tool.Type == openrouter.WebFetchType) {
				req.Tools = append(req.Tools, tool)
			}
		}
		if localAtStart {
			settings := s.cfg.Get()
			settings.AllowShellCommands = settings.AllowShellCommands && shellAtStart
			settings.AllowHiddenFiles = settings.AllowHiddenFiles && hiddenAtStart
			registry, err := toolRegistry(settings)
			if err != nil {
				return all, err
			}
			req.Tools = append(req.Tools, registry.Defs()...)
		}
		separator := text.Len() > 0
		res, streamErr := llm.Stream(ctx, req, func(delta string) {
			if delta == "" {
				return
			}
			if separator {
				onDelta("\n\n")
				separator = false
			}
			onDelta(delta)
		})
		if res.Text != "" {
			if text.Len() > 0 {
				text.WriteString("\n\n")
			}
			text.WriteString(res.Text)
		}
		if res.Model != "" {
			all.Model = res.Model
		}
		all.Fallback = all.Fallback || res.Fallback
		all.FinishReason = res.FinishReason
		// The last step's reasoning led to the answer that's saved.
		all.Reasoning = res.Reasoning
		for _, citation := range res.Citations {
			if citation.URL != "" && !seenCitations[citation.URL] {
				seenCitations[citation.URL] = true
				all.Citations = append(all.Citations, citation)
			}
		}
		if res.Searches != nil {
			if all.Searches == nil {
				all.Searches = new(int)
			}
			*all.Searches += *res.Searches
		}
		if err := ctx.Err(); err != nil {
			return all, err
		}
		if streamErr != nil || res.FinishReason != "tool_calls" {
			return all, streamErr
		}
		if step == maxToolSteps-1 {
			return all, fmt.Errorf("local tool loop stopped after %d model steps; try a narrower request", maxToolSteps)
		}
		if len(res.ToolCalls) == 0 {
			return all, errors.New("model requested tools without providing a call")
		}
		if len(res.ToolCalls) > maxLocalCalls-len(all.Activity) {
			return all, fmt.Errorf("local tool loop reached its %d-call limit; try a narrower request", maxLocalCalls)
		}
		for _, call := range res.ToolCalls {
			if call.ID == "" || seenCalls[call.ID] {
				return all, errors.New("model returned a missing or reused tool call ID")
			}
			seenCalls[call.ID] = true
		}
		if err := appendMessage(openrouter.Message{
			Role: "assistant", Content: res.Text, ToolCalls: res.ToolCalls,
			Reasoning: res.Reasoning, ReasoningDetails: res.ReasoningDetails,
		}); err != nil {
			return all, err
		}
		flush()
		for i, call := range res.ToolCalls {
			if err := ctx.Err(); err != nil {
				return all, err
			}
			localRan = true
			record := toolExecution{ToolActivity: ToolActivity{
				CallID: call.ID, Name: call.Function.Name, Summary: toolSummary(call, true), Status: "started",
			}, Call: call}
			if call.Function.Name == "run_command" {
				record.Status, record.Summary = "waiting", toolSummary(call, false)
			}
			s.events.Emit(EventChatTool, ToolEvent{ChatID: chatID, MessageID: answerID, Activity: record.ToolActivity})
			settings := s.cfg.Get()
			settings.AllowShellCommands = settings.AllowShellCommands && shellAtStart
			settings.AllowHiddenFiles = settings.AllowHiddenFiles && hiddenAtStart
			registry, err := toolRegistry(settings)
			if err != nil {
				return all, err
			}
			if !localAtStart || !settings.LocalTools {
				record.Output = "error: local tools are disabled"
				err = errors.New("local tools are disabled")
			} else {
				record.Output, err = registry.ExecuteWithApproval(ctx, call, func(ctx context.Context, approvedCall openrouter.ToolCall) error {
					if err := s.awaitApproval(ctx, approvalKey{chatID, answerID, approvedCall.ID}, approvedCall); err != nil {
						return err
					}
					record.Status, record.Summary = "started", toolSummary(approvedCall, true)
					s.events.Emit(EventChatTool, ToolEvent{ChatID: chatID, MessageID: answerID, Activity: record.ToolActivity})
					return nil
				})
			}
			record.Status = "finished"
			record.Summary = toolSummary(call, false)
			if err != nil {
				record.Status = "error"
			}
			if errors.Is(err, errApprovalDeclined) {
				record.Status = "denied"
			}
			if ctx.Err() != nil {
				record.Status = "stopped"
				record.Output = "error: operation canceled"
			}
			all.Activity = append(all.Activity, record)
			s.events.Emit(EventChatTool, ToolEvent{ChatID: chatID, MessageID: answerID, Activity: record.ToolActivity})
			if err := ctx.Err(); err != nil {
				return all, err
			}
			content := record.Output
			if limit.tokens > 0 {
				// Results left in this step share what's left of the window.
				room := (limit.toolRoom() - requestChars(req)) / (len(res.ToolCalls) - i)
				if len(content) > room {
					if room < minToolResultChars {
						return all, limit.full()
					}
					content = truncateUTF8(content, room-len(windowNote)) + windowNote
				}
			}
			if err := appendMessage(openrouter.Message{Role: "tool", ToolCallID: call.ID, Content: content}); err != nil {
				return all, err
			}
		}
	}
	return all, fmt.Errorf("local tool loop stopped after %d model steps", maxToolSteps)
}

func toolSummary(call openrouter.ToolCall, running bool) string {
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	path := args.Path
	verb := "Used"
	switch call.Function.Name {
	case "read_file":
		verb = "Read"
		if running {
			verb = "Reading"
		}
		if path != "" {
			path = filepath.Base(path)
		}
	case "list_directory":
		verb = "Listed"
		if running {
			verb = "Listing"
		}
	case "run_command":
		path, _ = tools.CommandText([]byte(call.Function.Arguments))
		verb = "Command"
		if running {
			verb = "Running"
		}
	default:
		if running {
			verb = "Using"
		}
		path = call.Function.Name
	}
	path = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, path)
	path = strings.Join(strings.Fields(path), " ")
	if len(path) > 160 {
		path = truncateUTF8(path, 160) + "…"
	}
	if path == "" {
		path = "tool"
	}
	return verb + " " + path
}
