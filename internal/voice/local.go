package voice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// LocalOptions identifies one immutable model configuration.
type LocalOptions struct {
	Engine, ModelDir, HelperPath, Language string
	Threads                                int
}

func ModelFiles(engine string) []string {
	switch engine {
	case "qwen3-asr":
		return []string{"conv_frontend.onnx", "encoder.int8.onnx", "decoder.int8.onnx", "tokenizer/vocab.json", "tokenizer/merges.txt", "tokenizer/tokenizer_config.json"}
	case "parakeet":
		return []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"}
	case "cohere-transcribe":
		return []string{"encoder.int8.onnx", "encoder.int8.onnx.data", "decoder.int8.onnx", "tokens.txt", "silero_vad.onnx"}
	}
	return nil
}

// LocalPaths checks files without loading a model or accessing audio hardware.
func LocalPaths(options LocalOptions) (helper, modelDir string, err error) {
	files := ModelFiles(options.Engine)
	if len(files) == 0 {
		return "", "", errors.New("unsupported local transcription model")
	}
	if options.Threads < 1 || options.Threads > 64 {
		return "", "", errors.New("choose between 1 and 64 CPU threads")
	}
	if options.Engine == "cohere-transcribe" && !slices.Contains(strings.Fields("en fr de it es pt el nl pl zh ja ko vi ar"), options.Language) {
		return "", "", errors.New("choose a supported Cohere transcription language")
	}
	if strings.TrimSpace(options.ModelDir) == "" {
		return "", "", errors.New("choose the downloaded model folder in Voice Preferences")
	}
	modelDir, err = filepath.Abs(expandHome(strings.TrimSpace(options.ModelDir)))
	if err != nil {
		return "", "", err
	}
	for _, name := range files {
		info, statErr := os.Stat(filepath.Join(modelDir, name))
		if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "", "", fmt.Errorf("model folder is missing a usable %s", name)
		}
	}
	executable, _ := os.Executable()
	helper, err = findLocalHelper(options.HelperPath, executable, exec.LookPath)
	if err != nil || helper == "" {
		return "", "", errors.New("native speech runtime is missing; install it or select its executable in Voice Preferences")
	}
	helper, err = filepath.Abs(helper)
	return helper, modelDir, err
}

func findLocalHelper(explicit, executable string, lookup func(string) (string, error)) (string, error) {
	if explicit != "" {
		return lookup(expandHome(strings.TrimSpace(explicit)))
	}
	var candidates []string
	if executable != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "ntikuma-asr"))
	}
	if runtime.GOOS == "linux" {
		candidates = append(candidates, "/usr/lib/ntikuma/ntikuma-asr")
	}
	candidates = append(candidates, "ntikuma-asr")
	for _, candidate := range candidates {
		if helper, err := lookup(candidate); err == nil {
			return helper, nil
		}
	}
	return "", exec.ErrNotFound
}

// CheckLocal also checks the helper protocol and dynamic-library availability.
func CheckLocal(ctx context.Context, options LocalOptions) error {
	helper, _, err := LocalPaths(options)
	if err != nil {
		return err
	}
	var output tailBuffer
	command := exec.CommandContext(ctx, helper, "--version")
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		detail := output.String()
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("native speech runtime could not start: %s", detail)
	}
	if !strings.Contains(output.String(), "ntikuma-asr protocol=1 sherpa=2.0.0") {
		return errors.New("native speech runtime has an unsupported version; rebuild or install the matching runtime")
	}
	if options.Engine == "cohere-transcribe" && !strings.Contains(output.String(), "cohere-transcribe") {
		return errors.New("native speech runtime does not support Cohere; rebuild or install the matching runtime")
	}
	return nil
}

type helperFrame struct {
	Protocol int    `json:"protocol,omitempty"`
	Ready    bool   `json:"ready,omitempty"`
	Text     string `json:"text,omitempty"`
	Error    string `json:"error,omitempty"`
}

type helperProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Scanner
	done   chan struct{}
	log    tailBuffer
}

// LocalTranscriber owns a warm native helper. Cancellation kills and reaps it,
// including when native inference cannot observe a Go context itself.
type LocalTranscriber struct {
	Options  LocalOptions
	requests sync.Mutex
	mu       sync.Mutex
	process  *helperProcess
	command  func(string, ...string) *exec.Cmd
}

func (t *LocalTranscriber) start(ctx context.Context) (*helperProcess, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if t.process != nil {
		select {
		case <-t.process.done:
			t.process = nil
		default:
			return t.process, false, nil
		}
	}
	helper, dir, err := LocalPaths(t.Options)
	if err != nil {
		return nil, false, err
	}
	command := t.command
	if command == nil {
		command = exec.Command
	}
	args := []string{"--engine", t.Options.Engine, "--model-dir", dir, "--threads", fmt.Sprint(t.Options.Threads)}
	if t.Options.Engine == "cohere-transcribe" {
		args = append(args, "--language", t.Options.Language)
	}
	cmd := command(helper, args...)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, false, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, false, err
	}
	p := &helperProcess{cmd: cmd, input: input, output: bufio.NewScanner(output), done: make(chan struct{})}
	p.output.Buffer(make([]byte, 4096), 1024*1024)
	cmd.Stderr = &p.log
	if err := cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, false, fmt.Errorf("start native speech runtime: %w", err)
	}
	t.process = p
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, true, nil
}

func (t *LocalTranscriber) Transcribe(ctx context.Context, wavPath string) (string, error) {
	t.requests.Lock()
	defer t.requests.Unlock()
	p, fresh, err := t.start(ctx)
	if err != nil {
		return "", err
	}
	read := func() (helperFrame, error) {
		if !p.output.Scan() {
			if err := p.output.Err(); err != nil {
				return helperFrame{}, err
			}
			return helperFrame{}, fmt.Errorf("native speech runtime stopped: %s", p.log.String())
		}
		var frame helperFrame
		if err := json.Unmarshal(p.output.Bytes(), &frame); err != nil {
			return frame, errors.New("invalid native speech runtime response")
		}
		if frame.Error != "" {
			return frame, errors.New(frame.Error)
		}
		return frame, nil
	}
	type response struct {
		text string
		err  error
	}
	result := make(chan response, 1)
	go func() {
		if fresh {
			frame, err := read()
			if err != nil {
				result <- response{err: err}
				return
			}
			if frame.Protocol != 1 || !frame.Ready {
				result <- response{err: errors.New("native speech runtime did not become ready")}
				return
			}
		}
		if err := json.NewEncoder(p.input).Encode(struct {
			WAVPath string `json:"wavPath"`
		}{wavPath}); err != nil {
			result <- response{err: err}
			return
		}
		frame, err := read()
		result <- response{text: frame.Text, err: err}
	}()
	select {
	case <-ctx.Done():
		t.Close()
		<-result
		return "", ctx.Err()
	case response := <-result:
		if err := ctx.Err(); err != nil {
			t.Close()
			return "", err
		}
		if response.err != nil {
			t.Close()
			return "", response.err
		}
		return strings.TrimSpace(response.text), nil
	}
}

func (t *LocalTranscriber) Close() {
	t.mu.Lock()
	p := t.process
	t.process = nil
	t.mu.Unlock()
	if p != nil {
		_ = p.input.Close()
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// Bound diagnostics even if a broken native helper floods stderr.
type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 8192 {
		b.data = append([]byte(nil), b.data[len(b.data)-8192:]...)
	}
	return len(p), nil
}
func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}
