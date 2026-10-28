package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ntikuma/internal/openrouter"
)

type approvalRecorder struct {
	Emitter
	approvals chan ApprovalEvent
}

func (r approvalRecorder) Emit(name string, data ...any) bool {
	result := r.Emitter.Emit(name, data...)
	if event, ok := data[0].(ApprovalEvent); ok {
		r.approvals <- event
	}
	return result
}

func commandCall(id, command string) openrouter.ToolCall {
	args, _ := json.Marshal(map[string]string{"command": command})
	return openrouter.ToolCall{ID: id, Type: "function", Function: openrouter.FunctionCall{Name: "run_command", Arguments: string(args)}}
}

func shellChat(t *testing.T, command string) (*ChatService, *recorder, chan ApprovalEvent, *[]openrouter.Request, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc, rec, _ := newChat(t, &fakeLLM{}, "synthetic-key")
	settings := svc.cfg.Get()
	settings.LocalTools = true
	settings.AllowShellCommands = true
	settings.ToolRoots = []string{}
	if err := svc.cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	events := make(chan ApprovalEvent, 64)
	svc.events = approvalRecorder{Emitter: rec, approvals: events}
	requests := useScript(svc, func(step int, _ context.Context, _ openrouter.Request, _ func(string)) (openrouter.Result, error) {
		if step == 0 {
			return openrouter.Result{FinishReason: "tool_calls", ToolCalls: []openrouter.ToolCall{commandCall("shell-1", command)}}, nil
		}
		return openrouter.Result{Text: "Command result received."}, nil
	})
	return svc, rec, events, requests, home
}

func pendingCommand(t *testing.T, events <-chan ApprovalEvent) ApprovalEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.State != "pending" {
			t.Fatalf("unexpected event: %+v", event)
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("missing approval event")
		return ApprovalEvent{}
	}
}

func decide(svc *ChatService, event ApprovalEvent, allow bool) error {
	return svc.Approve(event.ChatID, event.MessageID, event.CallID, allow)
}
func assertNoMarker(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(home, "marker")); !os.IsNotExist(err) {
		t.Fatal("command ran without approval")
	}
}

func TestShellCommandWaitsForExactScopedSingleUseApproval(t *testing.T) {
	command := "  printf 'approved' > \"$HOME/marker\"\n"
	svc, rec, events, requests, home := shellChat(t, command)
	res, err := svc.Send(SendRequest{Text: "Run a command"})
	if err != nil {
		t.Fatal(err)
	}
	event := pendingCommand(t, events)
	if event.Command != command || event.WorkingDirectory != home || event.ChatID != res.ChatID || event.MessageID != res.MessageID {
		t.Fatalf("approval: %+v", event)
	}
	assertNoMarker(t, home)
	if err := svc.Approve("other", event.MessageID, event.CallID, true); err == nil {
		t.Fatal("accepted wrong chat")
	}
	if err := svc.Approve(event.ChatID, "other", event.CallID, true); err == nil {
		t.Fatal("accepted wrong answer")
	}
	if err := svc.Approve(event.ChatID, event.MessageID, "other", true); err == nil {
		t.Fatal("accepted wrong call")
	}
	assertNoMarker(t, home)
	if err := decide(svc, event, true); err != nil {
		t.Fatal(err)
	}
	if err := decide(svc, event, true); err == nil {
		t.Fatal("reused approval")
	}
	done, ok := wait(t, rec).(DoneEvent)
	if !ok || len(done.Activity) != 1 || done.Activity[0].Status != "finished" {
		t.Fatalf("terminal: %+v", done)
	}
	if output, err := os.ReadFile(filepath.Join(home, "marker")); err != nil || string(output) != "approved" {
		t.Fatalf("result: %q %v", output, err)
	}
	messages := (*requests)[1].Messages
	if messages[len(messages)-1].Role != "tool" || messages[len(messages)-1].ToolCallID != event.CallID {
		t.Fatal("missing command continuation")
	}
	closed := <-events
	if closed.State != "closed" || closed.Outcome != "allowed" {
		t.Fatalf("closed: %+v", closed)
	}
}

func TestShellDenialAndExpiryReturnDeclinedWithoutExecution(t *testing.T) {
	for _, mode := range []string{"deny", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			svc, rec, events, requests, home := shellChat(t, `printf x > "$HOME/marker"`)
			if mode == "expiry" {
				svc.approvalTimeout = 40 * time.Millisecond
			}
			if _, err := svc.Send(SendRequest{Text: "Run"}); err != nil {
				t.Fatal(err)
			}
			event := pendingCommand(t, events)
			assertNoMarker(t, home)
			if mode == "deny" {
				if err := decide(svc, event, false); err != nil {
					t.Fatal(err)
				}
			}
			done, ok := wait(t, rec).(DoneEvent)
			if !ok || done.Activity[0].Status != "denied" {
				t.Fatalf("terminal: %+v", done)
			}
			assertNoMarker(t, home)
			messages := (*requests)[1].Messages
			if !strings.Contains(messages[len(messages)-1].Content, "the user declined") {
				t.Fatal("decline not returned to model")
			}
			closed := <-events
			want := "denied"
			if mode == "expiry" {
				want = "expired"
			}
			if closed.Outcome != want {
				t.Fatalf("closed: %+v", closed)
			}
			if err := decide(svc, event, true); err == nil {
				t.Fatal("approved a closed request")
			}
		})
	}
}

func TestEveryShellCallNeedsItsOwnApproval(t *testing.T) {
	svc, rec, events, _, home := shellChat(t, "")
	requests := useScript(svc, func(step int, _ context.Context, _ openrouter.Request, _ func(string)) (openrouter.Result, error) {
		if step == 0 {
			return openrouter.Result{FinishReason: "tool_calls", ToolCalls: []openrouter.ToolCall{commandCall("one", `printf x >> "$HOME/marker"`), commandCall("two", `printf y >> "$HOME/marker"`)}}, nil
		}
		return openrouter.Result{Text: "Finished"}, nil
	})
	if _, err := svc.Send(SendRequest{Text: "Run both"}); err != nil {
		t.Fatal(err)
	}
	first := pendingCommand(t, events)
	if err := decide(svc, first, true); err != nil {
		t.Fatal(err)
	}
	closed := <-events
	if closed.Outcome != "allowed" {
		t.Fatalf("closed: %+v", closed)
	}
	second := pendingCommand(t, events)
	if second.CallID == first.CallID {
		t.Fatal("shared approval")
	}
	if raw, err := os.ReadFile(filepath.Join(home, "marker")); err != nil || string(raw) != "x" {
		t.Fatalf("second command ran early: %q %v", raw, err)
	}
	if err := decide(svc, second, false); err != nil {
		t.Fatal(err)
	}
	done := wait(t, rec).(DoneEvent)
	if done.Activity[0].Status != "finished" || done.Activity[1].Status != "denied" || len(*requests) != 2 {
		t.Fatalf("terminal: %+v", done)
	}
}

func TestStopAndShutdownDismissApprovalAndKeepStoppedActivity(t *testing.T) {
	for _, mode := range []string{"stop", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			svc, rec, events, requests, home := shellChat(t, `printf x > "$HOME/marker"`)
			res, err := svc.Send(SendRequest{Text: "Run"})
			if err != nil {
				t.Fatal(err)
			}
			event := pendingCommand(t, events)
			if mode == "stop" {
				svc.Stop(res.ChatID)
			} else {
				if err := svc.ServiceShutdown(); err != nil {
					t.Fatal(err)
				}
			}
			done, ok := wait(t, rec).(DoneEvent)
			if !ok || !done.Stopped || done.Activity[0].Status != "stopped" || len(*requests) != 1 {
				t.Fatalf("terminal: %+v", done)
			}
			assertNoMarker(t, home)
			if err := decide(svc, event, true); err == nil {
				t.Fatal("accepted canceled approval")
			}
			closed := <-events
			if closed.Outcome != "canceled" {
				t.Fatalf("closed: %+v", closed)
			}
		})
	}
}

func TestShellPermissionRevocationPreventsPendingCommand(t *testing.T) {
	for _, mode := range []string{"shell-off", "local-off", "no-reply"} {
		t.Run(mode, func(t *testing.T) {
			svc, rec, events, _, home := shellChat(t, `printf x > "$HOME/marker"`)
			if _, err := svc.Send(SendRequest{Text: "Run"}); err != nil {
				t.Fatal(err)
			}
			event := pendingCommand(t, events)
			settings := svc.cfg.Get()
			if mode != "local-off" {
				settings.AllowShellCommands = false
			} else {
				settings.LocalTools = false
			}
			if err := svc.cfg.Save(settings); err != nil {
				t.Fatal(err)
			}
			if mode != "no-reply" {
				if err := decide(svc, event, true); err == nil {
					t.Fatal("allowed revoked command")
				}
			}
			wait(t, rec)
			assertNoMarker(t, home)
		})
	}
}

func TestInvalidCommandNeverOpensAnApproval(t *testing.T) {
	svc, rec, events, requests, home := shellChat(t, "")
	useScript(svc, func(step int, _ context.Context, _ openrouter.Request, _ func(string)) (openrouter.Result, error) {
		if step == 0 {
			return openrouter.Result{FinishReason: "tool_calls", ToolCalls: []openrouter.ToolCall{commandCall("invalid", "echo\x00bad")}}, nil
		}
		return openrouter.Result{Text: "Invalid command"}, nil
	})
	if _, err := svc.Send(SendRequest{Text: "Run"}); err != nil {
		t.Fatal(err)
	}
	done := wait(t, rec).(DoneEvent)
	if done.Activity[0].Status != "error" {
		t.Fatalf("terminal: %+v", done)
	}
	if len(events) != 0 {
		t.Fatal("invalid command prompted for approval")
	}
	assertNoMarker(t, home)
	_ = requests
}

func TestShellIsOffByDefaultAndCannotBeEnabledMidTurn(t *testing.T) {
	svc, rec, _ := newChat(t, &fakeLLM{}, "key")
	root := t.TempDir()
	enableFileTools(t, svc, root)
	requests := useScript(svc, func(step int, _ context.Context, _ openrouter.Request, _ func(string)) (openrouter.Result, error) {
		if step == 0 {
			settings := svc.cfg.Get()
			settings.AllowShellCommands = true
			if err := svc.cfg.Save(settings); err != nil {
				t.Error(err)
			}
			return openrouter.Result{FinishReason: "tool_calls", ToolCalls: []openrouter.ToolCall{commandCall("shell", "true")}}, nil
		}
		return openrouter.Result{Text: "Shell was not available"}, nil
	})
	if _, err := svc.Send(SendRequest{Text: "Run"}); err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	for _, req := range *requests {
		for _, tool := range req.Tools {
			if tool.Function != nil && tool.Function.Name == "run_command" {
				t.Fatal("shell enabled in existing turn")
			}
		}
	}
	last := (*requests)[1].Messages
	if !strings.Contains(last[len(last)-1].Content, "unknown tool") {
		t.Fatal("unadvertised shell call was accepted")
	}
}

func TestOldAnswerApprovalCannotDecideNewAnswerWithReusedCallID(t *testing.T) {
	svc, rec, events, _, home := shellChat(t, `printf x > "$HOME/marker"`)
	useScript(svc, func(step int, _ context.Context, _ openrouter.Request, _ func(string)) (openrouter.Result, error) {
		if step%2 == 0 {
			return openrouter.Result{FinishReason: "tool_calls", ToolCalls: []openrouter.ToolCall{commandCall("same-id", `printf x > "$HOME/marker"`)}}, nil
		}
		return openrouter.Result{Text: "Denied"}, nil
	})
	res, err := svc.Send(SendRequest{Text: "First"})
	if err != nil {
		t.Fatal(err)
	}
	old := pendingCommand(t, events)
	if err := decide(svc, old, false); err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
	<-events
	if _, err := svc.Send(SendRequest{ChatID: res.ChatID, Text: "Second"}); err != nil {
		t.Fatal(err)
	}
	current := pendingCommand(t, events)
	if err := decide(svc, old, true); err == nil {
		t.Fatal("old answer approved new command")
	}
	assertNoMarker(t, home)
	if err := decide(svc, current, false); err != nil {
		t.Fatal(err)
	}
	wait(t, rec)
}

func TestConcurrentApprovalRepliesConsumeOneDecision(t *testing.T) {
	svc, rec, events, _, _ := shellChat(t, "true")
	if _, err := svc.Send(SendRequest{Text: "Run"}); err != nil {
		t.Fatal(err)
	}
	event := pendingCommand(t, events)
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, allow := range []bool{true, false} {
		group.Go(func() { results <- decide(svc, event, allow) })
	}
	group.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d decisions", accepted)
	}
	wait(t, rec)
}

func TestShutdownCancelsRunningShellBeforeReturning(t *testing.T) {
	svc, rec, events, _, home := shellChat(t, `printf x > "$HOME/marker"; sleep 30`)
	if _, err := svc.Send(SendRequest{Text: "Run"}); err != nil {
		t.Fatal(err)
	}
	if err := decide(svc, pendingCommand(t, events), true); err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		if _, err := os.Stat(filepath.Join(home, "marker")); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := svc.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if !wait(t, rec).(DoneEvent).Stopped {
		t.Fatal("shutdown did not stop running command")
	}
	if _, err := svc.Send(SendRequest{Text: "After shutdown"}); err == nil {
		t.Fatal("accepted work after shutdown")
	}
}
