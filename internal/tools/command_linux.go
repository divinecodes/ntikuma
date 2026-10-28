package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Retain a fixed byte budget while draining both pipes. CombinedOutput would
// grow without bound before the registry could truncate the returned text.
type commandOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *commandOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	keep := min(len(p), MaxOutputChars*4-len(b.data))
	b.data = append(b.data, p[:keep]...)
	b.truncated = b.truncated || keep < len(p)
	return len(p), nil
}

func (b *commandOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := string(b.data)
	if b.truncated {
		text += "\n[truncated]"
	}
	return capOutput(text)
}

func commandEnvironment(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		// Noninteractive Bash can otherwise run BASH_ENV or imported functions
		// before the command the user actually approved.
		switch upper {
		case "NTIKUMA_OPENROUTER_KEY", "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "CDPATH", "GLOBIGNORE":
			continue
		}
		if strings.HasPrefix(upper, "BASH_FUNC_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func killCommandGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func runCommand(parent context.Context, text string, timeout time.Duration) (string, error) {
	if err := parent.Err(); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("home directory is unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-c", text)
	command.Dir = home
	command.Env = commandEnvironment(os.Environ())
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return killCommandGroup(command) }
	command.WaitDelay = 200 * time.Millisecond
	var output commandOutput
	command.Stdout, command.Stderr = &output, &output
	err = command.Run()
	// Clean up ordinary background children even if Bash exits successfully.
	_ = killCommandGroup(command)
	result := output.String()
	if parent.Err() != nil {
		return result, parent.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, fmt.Errorf("command timed out after %s\n%s", timeout, result)
	}
	if err != nil {
		return result, fmt.Errorf("command failed: %w\n%s", err, result)
	}
	return result, nil
}
