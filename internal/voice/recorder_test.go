package voice

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRecorderHelper(t *testing.T) {
	if os.Getenv("NTIKUMA_RECORDER_TEST") != "1" {
		return
	}
	path, mode := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT)
	size := 60
	if mode == "empty" {
		size = 44
	}
	data := make([]byte, size)
	if mode == "meter" {
		data = wavFixture(bytes.Repeat(sampleBytes(16384), 1600), true)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(path+".ready", nil, 0o600); err != nil {
		os.Exit(2)
	}
	<-signals
	os.Exit(0)
}

func TestRecorderClosesMeterOnStopAndCancel(t *testing.T) {
	r, path := helperRecorder(t, "meter")
	for _, cancel := range []bool{false, true} {
		if err := r.Start(); err != nil {
			t.Fatal(err)
		}
		waitRecorder(t, *path)
		before, err := os.ReadFile(*path)
		if err != nil {
			t.Fatal(err)
		}
		if level := r.Level(); !level.Available || level.Amplitude != 0.5 {
			t.Fatalf("level %+v", level)
		}
		meter := r.meter
		if cancel {
			r.Cancel()
		} else {
			out, err := r.Stop()
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(out)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("recording changed: %v", err)
			}
			os.Remove(out)
		}
		if _, err := meter.file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("meter file was not closed: %v", err)
		}
		if level := r.Level(); level.Available || level.Amplitude != 0 {
			t.Fatal("recording kept metering after cleanup")
		}
	}
}

func helperRecorder(t *testing.T, mode string) (*Recorder, *string) {
	t.Helper()
	r := NewRecorder(t.TempDir())
	var path string
	r.command = func(out string) (*exec.Cmd, error) {
		path = out
		cmd := exec.Command(os.Args[0], "-test.run=^TestRecorderHelper$", "--", out, mode)
		cmd.Env = append(os.Environ(), "NTIKUMA_RECORDER_TEST=1")
		return cmd, nil
	}
	t.Cleanup(r.Cancel)
	return r, &path
}

func waitRecorder(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path + ".ready"); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recorder helper did not become ready")
}

func TestRecorderStopCancelAndNoOverlap(t *testing.T) {
	r, path := helperRecorder(t, "audio")
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	waitRecorder(t, *path)
	if err := r.Start(); err == nil {
		t.Fatal("overlapping recording was allowed")
	}
	if on, _ := r.Recording(); !on {
		t.Fatal("recording not reported")
	}
	out, err := r.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if out != *path {
		t.Fatalf("wrong recording path: %s", out)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal("Stop removed the caller's recording")
	}
	os.Remove(out)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	waitRecorder(t, *path)
	r.Cancel()
	if _, err := os.Stat(*path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Cancel left a recording on disk")
	}
	if on, _ := r.Recording(); on {
		t.Fatal("recorder remained active after Cancel")
	}
}

func TestRecorderRejectsEmptyAudio(t *testing.T) {
	r, path := helperRecorder(t, "empty")
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	waitRecorder(t, *path)
	if _, err := r.Stop(); err == nil || !strings.Contains(err.Error(), "no audio captured") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(*path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty recording was not removed")
	}
}

func TestRecordCommandPreferenceAndMissingTools(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if _, err := recordCommand("out.wav"); err == nil {
		t.Fatal("missing tools not reported")
	}
	for _, tool := range []string{"parecord", "pw-record"} {
		path := filepath.Join(dir, tool)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		cmd, err := recordCommand("out.wav")
		if err != nil || cmd.Path != path {
			t.Fatalf("expected %s, got %v, %v", tool, cmd, err)
		}
	}
}
