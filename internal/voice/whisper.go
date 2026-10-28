package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Transcriber turns a WAV file into text. A cloud engine can implement this
// later without touching the UI.
type Transcriber interface {
	Transcribe(ctx context.Context, wavPath string) (string, error)
}

// WhisperServer talks to whisper.cpp's `whisper-server` HTTP endpoint.
//
// Start one yourself:
//
//	whisper-server -m ~/models/ggml-base.en.bin --host 127.0.0.1 --port 8178
//
// or set Bin and ModelPath and Ntikuma will start it on first use.
type WhisperServer struct {
	BaseURL   string
	Language  string
	Bin       string
	ModelPath string
	HTTP      *http.Client

	mu       sync.Mutex
	proc     *exec.Cmd
	procDone chan struct{}
}

// NewWhisperServer returns a client for baseURL.
func NewWhisperServer(baseURL, language string) *WhisperServer {
	return &WhisperServer{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Language: language,
		HTTP:     &http.Client{Timeout: 2 * time.Minute},
	}
}

// Transcribe uploads wavPath to /inference and returns the text.
func (w *WhisperServer) Transcribe(ctx context.Context, wavPath string) (string, error) {
	if err := w.ensureRunning(ctx); err != nil {
		return "", err
	}
	f, err := os.Open(wavPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	_ = mw.WriteField("response_format", "json")
	_ = mw.WriteField("temperature", "0.0")
	if w.Language != "" {
		_ = mw.WriteField("language", w.Language)
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.BaseURL+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper-server: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("whisper-server response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper-server: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("whisper-server: bad response: %w", err)
	}
	if out.Error != "" {
		return "", errors.New("whisper-server: " + out.Error)
	}
	return cleanTranscript(out.Text), nil
}

// cleanTranscript trims whitespace and drops whisper's markers for silence.
func cleanTranscript(s string) string {
	s = strings.TrimSpace(s)
	for _, marker := range []string{"[BLANK_AUDIO]", "[ Silence ]", "(silence)"} {
		s = strings.ReplaceAll(s, marker, "")
	}
	return strings.Join(strings.Fields(s), " ")
}

// Prefer readiness over the public page, which may not be installed.
func (w *WhisperServer) healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	for _, path := range []string{"/health", "/"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.BaseURL+path, nil)
		if err != nil {
			return false
		}
		resp, err := w.HTTP.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		if path == "/health" && resp.StatusCode == http.StatusNotFound {
			continue
		}
		return resp.StatusCode == http.StatusOK
	}
	return false
}

// CheckSetup checks reachability or startup paths without launching a server.
func (w *WhisperServer) CheckSetup(ctx context.Context) (string, error) {
	u, err := url.Parse(w.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errors.New("enter a valid whisper-server http or https address in Preferences")
	}
	if w.healthy(ctx) {
		return "Whisper server is reachable. Its model is configured on the server.", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if w.Bin == "" || w.ModelPath == "" {
		return "", errors.New("Whisper server is not reachable; start it or choose its executable and model paths")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || u.Port() == "" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return "", errors.New("managed whisper-server needs an http address with an explicit local port")
	}
	if _, err := exec.LookPath(expandHome(w.Bin)); err != nil {
		return "", errors.New("Whisper server executable is missing or not executable")
	}
	info, err := os.Stat(expandHome(w.ModelPath))
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", errors.New("Whisper model file is missing or empty")
	}
	return "Whisper startup files are available. The server starts when transcription begins.", nil
}

// ensureRunning starts whisper-server when it isn't up and Bin/ModelPath are set.
func (w *WhisperServer) ensureRunning(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	u, err := url.Parse(w.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("enter a valid whisper-server http or https address in Preferences")
	}
	if w.healthy(ctx) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.Bin == "" || w.ModelPath == "" {
		return fmt.Errorf("whisper-server is not ready at %s; start whisper-server or set its path in Preferences", w.BaseURL)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "http" || u.Port() == "" || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return errors.New("managed whisper-server needs an http address with an explicit local port")
	}
	w.mu.Lock()
	if err := ctx.Err(); err != nil {
		w.mu.Unlock()
		return err
	}
	if w.proc == nil {
		modelPath := expandHome(w.ModelPath)
		if _, err := os.Stat(modelPath); err != nil {
			w.mu.Unlock()
			return fmt.Errorf("Whisper model: %w", err)
		}
		args := []string{"-m", modelPath, "--host", host, "--port", u.Port()}
		if path := strings.TrimRight(u.Path, "/"); path != "" {
			args = append(args, "--request-path", path)
		}
		cmd := exec.Command(expandHome(w.Bin), args...)
		if err := cmd.Start(); err != nil {
			w.mu.Unlock()
			return fmt.Errorf("start whisper-server: %w", err)
		}
		w.proc = cmd
		done := make(chan struct{})
		w.procDone = done
		go func() {
			_ = cmd.Wait()
			w.mu.Lock()
			w.proc, w.procDone = nil, nil
			close(done)
			w.mu.Unlock()
		}()
	}
	done := w.procDone
	w.mu.Unlock()

	deadline := time.Now().Add(15 * time.Second) // loading a model takes a moment
	for time.Now().Before(deadline) {
		if w.healthy(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			w.Close()
			return ctx.Err()
		case <-done:
			return errors.New("whisper-server exited before becoming ready; check its executable and model path")
		case <-time.After(300 * time.Millisecond):
		}
	}
	w.Close()
	return errors.New("whisper-server did not come up within 15s")
}

// Close stops a whisper-server Ntikuma started.
func (w *WhisperServer) Close() {
	w.mu.Lock()
	done := w.procDone
	if w.proc != nil && w.proc.Process != nil {
		_ = w.proc.Process.Kill()
	}
	w.mu.Unlock()
	if done != nil {
		<-done
	}
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
