package voice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt in with existing public fixtures and downloaded models. Ordinary tests
// never download models, start real speech runtimes, or use a microphone.
func TestNativeLocalModels(t *testing.T) {
	root := os.Getenv("NTIKUMA_NATIVE_ASR_ROOT")
	if root == "" {
		t.Skip("set NTIKUMA_NATIVE_ASR_ROOT for native model integration")
	}
	models := map[string]string{"qwen3-asr": "sherpa-onnx-qwen3-asr-0.6B-int8-2026-03-25", "parakeet": "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8"}
	if os.Getenv("NTIKUMA_TEST_COHERE") == "1" {
		models["cohere-transcribe"] = "sherpa-onnx-cohere-transcribe-14-lang-int8-2026-04-01"
	}
	for engine, folder := range models {
		t.Run(engine, func(t *testing.T) {
			backend := &LocalTranscriber{Options: LocalOptions{Engine: engine,
				ModelDir: filepath.Join(root, "build/asr-eval/models", folder), Threads: 4,
				HelperPath: filepath.Join(root, "bin/ntikuma-asr"), Language: "en"}}
			t.Cleanup(backend.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			wav := filepath.Join(root, "build/asr-eval/smoke/clean.wav")
			if err := CheckLocal(ctx, backend.Options); err != nil {
				t.Fatal(err)
			}
			for repeat := 0; repeat < 2; repeat++ {
				text, err := backend.Transcribe(ctx, wav)
				if err != nil || !strings.Contains(strings.ToLower(text), "country") {
					t.Fatalf("native transcription: %q %v", text, err)
				}
			}
			if engine == "cohere-transcribe" {
				for _, clip := range []string{"silence-2", "quiet", "silence-5", "noise", "clean"} {
					text, err := backend.Transcribe(ctx, filepath.Join(root, "build/asr-eval/smoke", clip+".wav"))
					if err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(clip, "silence") {
						if text != "" {
							t.Fatalf("invented speech on %s: %q", clip, text)
						}
					} else if !strings.Contains(strings.ToLower(text), "country") {
						t.Fatalf("lost %s speech: %q", clip, text)
					}
				}
			}
			backend.mu.Lock()
			process := backend.process
			backend.mu.Unlock()
			canceled, stop := context.WithCancel(context.Background())
			timer := time.AfterFunc(10*time.Millisecond, stop)
			_, err := backend.Transcribe(canceled, wav)
			timer.Stop()
			stop()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("native cancellation: %v", err)
			}
			select {
			case <-process.done:
			default:
				t.Fatal("native child survived cancellation")
			}
			if text, err := backend.Transcribe(ctx, wav); err != nil || !strings.Contains(strings.ToLower(text), "country") {
				t.Fatalf("native restart: %q %v", text, err)
			}
		})
	}
}
