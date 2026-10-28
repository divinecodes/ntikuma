package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"ntikuma/internal/openrouter"
	"ntikuma/internal/tools"
)

var errApprovalDeclined = errors.New("the user declined")

type approvalKey struct{ chatID, messageID, callID string }
type pendingApproval struct {
	ctx      context.Context
	expires  time.Time
	decision chan bool
	decided  bool
}

// Approve consumes one decision for this exact answer/call. The command comes
// from the pending call, never from a frontend-supplied replacement string.
func (s *ChatService) Approve(chatID, messageID, callID string, allow bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := approvalKey{chatID, messageID, callID}
	pending := s.approvals[key]
	answer := s.running[chatID]
	if pending == nil || pending.decided || pending.ctx.Err() != nil || !time.Now().Before(pending.expires) || answer == nil || answer.messageID != messageID || s.closed {
		return errors.New("this command approval is no longer active")
	}
	settings := s.cfg.Get()
	if allow && (!settings.LocalTools || !settings.AllowShellCommands) {
		pending.decided = true
		pending.decision <- false
		return errors.New("shell commands are disabled")
	}
	pending.decided = true
	pending.decision <- allow
	return nil
}

func (s *ChatService) awaitApproval(ctx context.Context, key approvalKey, call openrouter.ToolCall) (err error) {
	command, err := tools.CommandText([]byte(call.Function.Arguments))
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("home directory is unavailable")
	}
	pending := &pendingApproval{ctx: ctx, expires: time.Now().Add(s.approvalTimeout), decision: make(chan bool, 1)}
	s.mu.Lock()
	answer := s.running[key.chatID]
	if s.closed || ctx.Err() != nil || answer == nil || answer.messageID != key.messageID || s.approvals[key] != nil {
		s.mu.Unlock()
		return errors.New("command approval belongs to an inactive answer")
	}
	s.approvals[key] = pending
	s.mu.Unlock()
	event := ApprovalEvent{ChatID: key.chatID, MessageID: key.messageID, CallID: key.callID, Command: command, WorkingDirectory: home, ExpiresAt: pending.expires, State: "pending"}
	outcome := "canceled"
	defer func() {
		s.mu.Lock()
		if s.approvals[key] == pending {
			delete(s.approvals, key)
		}
		s.mu.Unlock()
		event.State, event.Outcome = "closed", outcome
		s.events.Emit(EventChatApproval, event)
	}()
	s.events.Emit(EventChatApproval, event)
	timer := time.NewTimer(time.Until(pending.expires))
	defer timer.Stop()
	permissions := time.NewTicker(200 * time.Millisecond)
	defer permissions.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		settings := s.cfg.Get()
		if !settings.LocalTools || !settings.AllowShellCommands {
			outcome = "revoked"
			return errors.New("shell commands are disabled")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			outcome = "expired"
			return fmt.Errorf("%w (approval expired)", errApprovalDeclined)
		case allow := <-pending.decision:
			if !allow {
				outcome = "denied"
				return errApprovalDeclined
			}
			settings := s.cfg.Get()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !settings.LocalTools || !settings.AllowShellCommands {
				outcome = "revoked"
				return errors.New("shell commands are disabled")
			}
			if !time.Now().Before(pending.expires) {
				outcome = "expired"
				return fmt.Errorf("%w (approval expired)", errApprovalDeclined)
			}
			outcome = "allowed"
			return nil
		case <-permissions.C:
		}
	}
}
