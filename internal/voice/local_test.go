package voice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeHelperFixture(t *testing.T) {
	mode := os.Getenv("NTIKUMA_NATIVE_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "loading" {
		for {
			time.Sleep(time.Second)
		}
	}
	if mode == "invalid" {
		fmt.Println(`{"ready":true,"protocol":9}`)
		for {
			time.Sleep(time.Second)
		}
	}
	fmt.Println(`{"ready":true,"protocol":1}`)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if mode == "decoding" {
			_ = os.WriteFile(os.Getenv("NTIKUMA_NATIVE_MARKER"), []byte("started"), 0600)
			for {
				time.Sleep(time.Second)
			}
		}
		if mode == "crash" {
			fmt.Fprintln(os.Stderr, "fixture native failure")
			os.Exit(7)
		}
		var request struct {
			WAVPath string `json:"wavPath"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		_ = json.NewEncoder(os.Stdout).Encode(helperFrame{Text: fmt.Sprintf("%d %s", os.Getpid(), request.WAVPath)})
	}
	os.Exit(0)
}

func TestLocalHelperDiscoveryPreservesOverridesAndFindsPackagedRuntime(t *testing.T) {
	const adjacent = "/checkout/bin/ntikuma-asr"
	const packaged = "/usr/lib/ntikuma/ntikuma-asr"
	for _, tc := range []struct {
		name, explicit, want string
		available            []string
	}{
		{"explicit wins", "/custom/helper", "/custom/helper", []string{"/custom/helper", adjacent, packaged, "ntikuma-asr"}},
		{"missing override never falls back", "/missing/helper", "", []string{adjacent, packaged}},
		{"development helper wins", "", adjacent, []string{adjacent, packaged, "ntikuma-asr"}},
		{"system package precedes PATH", "", packaged, []string{packaged, "ntikuma-asr"}},
		{"PATH remains supported", "", "ntikuma-asr", []string{"ntikuma-asr"}},
		{"missing runtime", "", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(path string) (string, error) {
				for _, available := range tc.available {
					if path == available {
						return path, nil
					}
				}
				return "", exec.ErrNotFound
			}
			got, err := findLocalHelper(tc.explicit, "/checkout/bin/ntikuma", lookup)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("helper=%q error=%v; want %q", got, err, tc.want)
			}
		})
	}
}

func localFixture(t *testing.T, mode string) *LocalTranscriber {
	t.Helper()
	dir := t.TempDir()
	for _, name := range ModelFiles("parakeet") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	transcriber := &LocalTranscriber{Options: LocalOptions{Engine: "parakeet", ModelDir: dir, Threads: 4, HelperPath: os.Args[0]}}
	transcriber.command = func(_ string, _ ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeHelperFixture$")
		cmd.Env = append(os.Environ(), "NTIKUMA_NATIVE_FIXTURE="+mode, "NTIKUMA_NATIVE_MARKER="+filepath.Join(dir, "decoding"))
		return cmd
	}
	t.Cleanup(transcriber.Close)
	return transcriber
}

func TestLocalTranscriberReusesItsHelperAndRestartsAfterClose(t *testing.T) {
	transcriber := localFixture(t, "normal")
	first, err := transcriber.Transcribe(context.Background(), "one.wav")
	if err != nil {
		t.Fatal(err)
	}
	second, err := transcriber.Transcribe(context.Background(), "two.wav")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Fields(first)[0] != strings.Fields(second)[0] || !strings.HasSuffix(second, "two.wav") {
		t.Fatalf("warm reuse failed: %q %q", first, second)
	}
	transcriber.mu.Lock()
	process := transcriber.process
	transcriber.mu.Unlock()
	transcriber.Close()
	select {
	case <-process.done:
	default:
		t.Fatal("helper was not reaped")
	}
	third, err := transcriber.Transcribe(context.Background(), "three.wav")
	if err != nil || strings.Fields(first)[0] == strings.Fields(third)[0] {
		t.Fatalf("restart failed: %q %v", third, err)
	}
}

func TestCohereReadinessRequiresExternalWeightsVADAndCapableRuntime(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "ntikuma-asr")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho 'ntikuma-asr protocol=1 sherpa=2.0.0'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	options := LocalOptions{Engine: "cohere-transcribe", ModelDir: dir, Threads: 4, Language: "en", HelperPath: helper}
	for _, name := range ModelFiles(options.Engine) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, missing := range []string{"encoder.int8.onnx.data", "silero_vad.onnx"} {
		name := filepath.Join(dir, missing)
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LocalPaths(options); err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("missing %s: %v", missing, err)
		}
		if err := os.WriteFile(name, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckLocal(context.Background(), options); err == nil || !strings.Contains(err.Error(), "does not support Cohere") {
		t.Fatalf("old runtime: %v", err)
	}
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho 'ntikuma-asr protocol=1 sherpa=2.0.0 engines=cohere-transcribe'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckLocal(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.Language = "auto"
	if _, _, err := LocalPaths(options); err == nil {
		t.Fatal("accepted automatic Cohere language")
	}
}

func TestCohereTranscriberPassesItsSavedLanguageToTheHelper(t *testing.T) {
	backend := localFixture(t, "normal")
	backend.Options.Engine, backend.Options.Language = "cohere-transcribe", "fr"
	for _, name := range ModelFiles(backend.Options.Engine) {
		if err := os.WriteFile(filepath.Join(backend.Options.ModelDir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	original := backend.command
	var received []string
	backend.command = func(name string, args ...string) *exec.Cmd {
		received = append([]string(nil), args...)
		return original(name, args...)
	}
	if _, err := backend.Transcribe(context.Background(), "clip.wav"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(received, " "), "--language fr") {
		t.Fatalf("saved language was not forwarded: %v", received)
	}
}

func TestLocalCancellationKillsLoadingAndUnresponsiveInference(t *testing.T) {
	for _, mode := range []string{"loading", "decoding"} {
		t.Run(mode, func(t *testing.T) {
			transcriber := localFixture(t, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := transcriber.Transcribe(ctx, "audio.wav"); done <- err }()
			deadline := time.Now().Add(3 * time.Second)
			var process *helperProcess
			for time.Now().Before(deadline) {
				transcriber.mu.Lock()
				process = transcriber.process
				transcriber.mu.Unlock()
				_, marker := os.Stat(filepath.Join(transcriber.Options.ModelDir, "decoding"))
				if process != nil && (mode == "loading" || marker == nil) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if process == nil {
				t.Fatal("helper did not start")
			}
			if mode == "decoding" {
				if _, err := os.Stat(filepath.Join(transcriber.Options.ModelDir, "decoding")); err != nil {
					t.Fatal("inference did not begin")
				}
			}
			cancel()
			// The voice service also closes an owned backend after canceling
			// its context. Pipe closure must not hide context.Canceled.
			transcriber.Close()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation waited for native inference")
			}
			select {
			case <-process.done:
			default:
				t.Fatal("canceled helper was not reaped")
			}
		})
	}
}

func TestLocalHelperFailuresAreBoundedAndRecoverable(t *testing.T) {
	for _, mode := range []string{"invalid", "crash"} {
		t.Run(mode, func(t *testing.T) {
			transcriber := localFixture(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := transcriber.Transcribe(ctx, "audio.wav"); err == nil {
				t.Fatal("bad helper succeeded")
			}
			transcriber.mu.Lock()
			process := transcriber.process
			transcriber.mu.Unlock()
			if process != nil {
				t.Fatal("failed helper retained")
			}
		})
	}
	transcriber := localFixture(t, "normal")
	if err := os.Remove(filepath.Join(transcriber.Options.ModelDir, "tokens.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := transcriber.Transcribe(context.Background(), "audio.wav"); err == nil || !strings.Contains(err.Error(), "tokens.txt") {
		t.Fatalf("missing model: %v", err)
	}
}

func TestLocalReadinessChecksRuntimeProtocolWithoutLoadingWeights(t *testing.T) {
	transcriber := localFixture(t, "normal")
	helper := filepath.Join(t.TempDir(), "helper with spaces")
	for _, output := range []string{"ntikuma-asr protocol=1 sherpa=2.0.0", "incompatible runtime"} {
		if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' '"+output+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		transcriber.Options.HelperPath = helper
		err := CheckLocal(context.Background(), transcriber.Options)
		if (err == nil) != strings.HasPrefix(output, "ntikuma-asr") {
			t.Fatalf("readiness for %q: %v", output, err)
		}
	}
}
