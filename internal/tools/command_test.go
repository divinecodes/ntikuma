//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"ntikuma/internal/openrouter"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func shellCall(command string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"command": command})
	return raw
}

func TestCommandArgumentsPreserveExactTextAndRejectHiddenCharacters(t *testing.T) {
	command := "  printf 'first'\n\tprintf 'second'  "
	if got, err := CommandText(shellCall(command)); err != nil || got != command {
		t.Fatalf("%q %v", got, err)
	}
	for _, raw := range []string{`null`, `{}`, `[]`, `{"command":4}`, `{"command":"true","extra":"x"}`, `{"command":"true"} {}`, string(shellCall(" ")), string(shellCall("echo\x00bad")), string(shellCall("echo\rhidden")), string(shellCall("echo\u202ebad")), string(shellCall("echo\x1b[31m")), string(shellCall(strings.Repeat("x", MaxCommandBytes+1)))} {
		if _, err := CommandText([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw[:min(len(raw), 90)])
		}
	}
}

func TestShellRunsInHomeWithBoundedCombinedOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tool := commandTool{timeout: time.Second}
	output, err := tool.Run(context.Background(), shellCall("pwd; printf 'stderr' >&2"))
	if err != nil || !strings.Contains(output, home) || !strings.Contains(output, "stderr") {
		t.Fatalf("%q %v", output, err)
	}
	output, err = tool.Run(context.Background(), shellCall("printf '%100000s' x; printf '%100000s' y >&2"))
	if err != nil || utf8.RuneCountInString(output) > MaxOutputChars || !strings.HasSuffix(output, "[truncated]") {
		t.Fatalf("output bytes %d, error %v", len(output), err)
	}
	_, err = tool.Run(context.Background(), shellCall("printf 'failure output'; printf 'stderr output' >&2; exit 7"))
	if err == nil || !strings.Contains(err.Error(), "exit status 7") || !strings.Contains(err.Error(), "failure output") || !strings.Contains(err.Error(), "stderr output") {
		t.Fatalf("missing exit output: %v", err)
	}
}

func TestShellEnvironmentOmitsAppKeyAndStartupHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	startup := filepath.Join(home, "startup.sh")
	if err := os.WriteFile(startup, []byte(`printf injected > "$HOME/injected"`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTIKUMA_OPENROUTER_KEY", "synthetic-secret")
	t.Setenv("BASH_ENV", startup)
	t.Setenv("ENV", startup)
	output, err := ShellTool().Run(context.Background(), shellCall(`printf '%s|%s|%s' "${NTIKUMA_OPENROUTER_KEY-unset}" "${BASH_ENV-unset}" "${ENV-unset}"`))
	if err != nil || output != "unset|unset|unset" {
		t.Fatalf("unsafe environment result %q %v", output, err)
	}
	if _, err := os.Stat(filepath.Join(home, "injected")); !os.IsNotExist(err) {
		t.Fatal("shell startup hook ran")
	}
	filtered := commandEnvironment([]string{"PATH=/bin", "ntikuma_openrouter_key=hidden", "BASH_FUNC_injected%%=() { echo injected; }", "SHELLOPTS=x", "HOME=/tmp"})
	if strings.Join(filtered, "|") != "PATH=/bin|HOME=/tmp" {
		t.Fatalf("filter: %v", filtered)
	}
}

func TestShellRequiresApprovalAndChecksCancellationAgainAfterGrant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	registry := &Registry{}
	if err := registry.Register(ShellTool()); err != nil {
		t.Fatal(err)
	}
	call := toolCall("run_command", string(shellCall(`printf x > "$HOME/marker"`)))
	if output := registry.Run(context.Background(), call); !strings.Contains(output, "approval is required") {
		t.Fatal(output)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := registry.ExecuteWithApproval(ctx, call, func(context.Context, openrouter.ToolCall) error { cancel(); return nil }); err == nil {
		t.Fatal("executed after cancellation")
	}
	if _, err := os.Stat(filepath.Join(home, "marker")); !os.IsNotExist(err) {
		t.Fatal("command ran without an active approval")
	}
	if _, err := registry.ExecuteWithApproval(context.Background(), call, func(context.Context, openrouter.ToolCall) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "marker")); err != nil {
		t.Fatal("approved command did not run", err)
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for time.Now().Before(end) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("child did not start")
	return 0
}

func assertProcessStopped(t *testing.T, pid int) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if os.IsNotExist(err) {
			return
		}
		if err == nil {
			tail := string(raw[strings.LastIndex(string(raw), ")")+1:])
			if strings.HasPrefix(strings.TrimSpace(tail), "Z ") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child %d is still running", pid)
}

func TestShellCancellationKillsTheProcessGroup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ShellTool().Run(ctx, shellCall(`sleep 30 & child=$!; printf '%s' "$child" > "$HOME/child.pid"; wait`))
		done <- err
	}()
	pid := waitForPID(t, filepath.Join(home, "child.pid"))
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("command did not cancel promptly")
	}
	assertProcessStopped(t, pid)
}

func TestShellTimeoutAndExitedParentCleanUpChildren(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tool := commandTool{timeout: 100 * time.Millisecond}
	_, err := tool.Run(context.Background(), shellCall(`sleep 30 & child=$!; printf '%s' "$child" > "$HOME/timeout.pid"; printf 'began'; wait`))
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "began") {
		t.Fatalf("timeout: %v", err)
	}
	pid := waitForPID(t, filepath.Join(home, "timeout.pid"))
	assertProcessStopped(t, pid)
	_, _ = ShellTool().Run(context.Background(), shellCall(`sleep 30 & printf '%s' "$!" > "$HOME/background.pid"`))
	assertProcessStopped(t, waitForPID(t, filepath.Join(home, "background.pid")))
}
