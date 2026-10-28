package voice

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTranscribeUploadsMultipart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/" {
			return // health check
		}
		if r.URL.Path != "/inference" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		if string(b) != "RIFFfake" {
			t.Errorf("file body %q", b)
		}
		if r.FormValue("language") != "en" || r.FormValue("response_format") != "json" {
			t.Errorf("form %v", r.Form)
		}
		w.Write([]byte(`{"text":"  What's the cleanest way to stream [BLANK_AUDIO] an OpenRouter response in Go?\n"}`))
	}))
	defer srv.Close()

	wav := filepath.Join(t.TempDir(), "a.wav")
	os.WriteFile(wav, []byte("RIFFfake"), 0o600)

	got, err := NewWhisperServer(srv.URL, "en").Transcribe(context.Background(), wav)
	if err != nil {
		t.Fatal(err)
	}
	want := "What's the cleanest way to stream an OpenRouter response in Go?"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestTranscribeWithoutServerExplains(t *testing.T) {
	w := NewWhisperServer("http://127.0.0.1:1", "en")
	_, err := w.Transcribe(context.Background(), "/nonexistent.wav")
	if err == nil || !strings.Contains(err.Error(), "start whisper-server") {
		t.Fatalf("got %v", err)
	}
}

func TestWhisperHelper(t *testing.T) {
	if os.Getenv("NTIKUMA_WHISPER_HELPER") != "1" {
		return
	}
	var host, port string
	for i, arg := range os.Args {
		if arg == "--host" {
			host = os.Args[i+1]
		}
		if arg == "--port" {
			port = os.Args[i+1]
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok"}`)) })
	if err := http.ListenAndServe(net.JoinHostPort(host, port), mux); err != nil {
		os.Exit(2)
	}
}

func TestManagedWhisperStartsAndCloseReapsOnlyOwnedProcess(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(model, []byte("fake model"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "whisper-server")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec \"$NTIKUMA_WHISPER_TEST_BINARY\" -test.run=^TestWhisperHelper$ -- \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTIKUMA_WHISPER_HELPER", "1")
	t.Setenv("NTIKUMA_WHISPER_TEST_BINARY", os.Args[0])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	w := NewWhisperServer("http://"+address, "en")
	w.Bin, w.ModelPath = bin, model
	t.Cleanup(w.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.ensureRunning(ctx); err != nil {
		t.Fatal(err)
	}
	if !w.healthy(ctx) {
		t.Fatal("managed server never became ready")
	}
	w.Close()
	w.mu.Lock()
	active := w.proc != nil
	w.mu.Unlock()
	if active {
		t.Fatal("owned process was not reaped")
	}
	if w.healthy(ctx) {
		t.Fatal("managed server still answers after Close")
	}
	// An externally started server has no owned process to kill.
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer external.Close()
	client := NewWhisperServer(external.URL, "en")
	client.Close()
	if !client.healthy(ctx) {
		t.Fatal("Close affected the external server")
	}
}

func TestWhisperStartupErrorsAndCanceledContext(t *testing.T) {
	for _, test := range []struct{ name, bin, model, want string }{
		{"model missing", "/bin/false", "/nonexistent/model.bin", "Whisper model"},
		{"executable missing", "/nonexistent/whisper-server", "model", "start whisper-server"},
		{"early exit", "/bin/false", "model", "exited before becoming ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := NewWhisperServer("http://127.0.0.1:1", "en")
			w.Bin, w.ModelPath = test.bin, test.model
			if test.model == "model" {
				w.ModelPath = filepath.Join(t.TempDir(), "model.bin")
				if err := os.WriteFile(w.ModelPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(w.Close)
			if err := w.ensureRunning(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewWhisperServer("http://127.0.0.1:1", "en").ensureRunning(ctx); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}

func TestWhisperHTTPAndMalformedResponseErrors(t *testing.T) {
	for _, test := range []struct {
		name, body string
		code       int
	}{
		{"HTTP error", "model unavailable", http.StatusServiceUnavailable},
		{"malformed JSON", "not json", http.StatusOK},
		{"server error", `{"error":"inference failed"}`, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					return
				}
				w.WriteHeader(test.code)
				w.Write([]byte(test.body))
			}))
			defer srv.Close()
			wav := filepath.Join(t.TempDir(), "audio.wav")
			if err := os.WriteFile(wav, []byte("fake audio"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewWhisperServer(srv.URL, "en").Transcribe(context.Background(), wav); err == nil {
				t.Fatal("expected a transcription error")
			}
		})
	}
}
