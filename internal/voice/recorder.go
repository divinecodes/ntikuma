// Package voice records from the microphone and turns speech into text.
//
// Recording shells out to PipeWire's pw-record (falling back to PulseAudio's
// parecord), so the microphone never goes through the webview. Transcription
// goes through the Transcriber interface; WhisperServer talks to a local
// whisper.cpp server.
package voice

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ErrNotRecording is returned by Stop when nothing is being recorded.
var ErrNotRecording = errors.New("not recording")

// Recorder captures 16 kHz mono WAV, the format whisper.cpp wants.
type Recorder struct {
	Dir string // where temporary WAV files go

	mu      sync.Mutex
	cmd     *exec.Cmd
	path    string
	start   time.Time
	done    chan error
	command func(string) (*exec.Cmd, error)
	meter   *wavMeter
}

// NewRecorder stores recordings under dir.
func NewRecorder(dir string) *Recorder { return &Recorder{Dir: dir} }

// recordCommand picks pw-record, then parecord.
func recordCommand(out string) (*exec.Cmd, error) {
	if p, err := exec.LookPath("pw-record"); err == nil {
		return exec.Command(p, "--rate", "16000", "--channels", "1", "--format", "s16", out), nil
	}
	if p, err := exec.LookPath("parecord"); err == nil {
		return exec.Command(p, "--rate=16000", "--channels=1", "--format=s16le", "--file-format=wav", out), nil
	}
	return nil, errors.New("neither pw-record nor parecord found; install pipewire-bin or pulseaudio-utils")
}

// Start begins recording. Calling Start while recording is an error.
func (r *Recorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return errors.New("already recording")
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(r.Dir, fmt.Sprintf("dictation-%d.wav", time.Now().UnixNano()))
	command := r.command
	if command == nil {
		command = recordCommand
	}
	cmd, err := command(path)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return fmt.Errorf("start recorder: %w", err)
	}
	r.cmd, r.path, r.start = cmd, path, time.Now()
	r.meter = &wavMeter{}
	r.done = make(chan error, 1)
	go func(c *exec.Cmd, done chan error) { done <- c.Wait() }(cmd, r.done)
	return nil
}

// Recording reports whether a recording is in progress and for how long.
func (r *Recorder) Recording() (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		return false, 0
	}
	return true, time.Since(r.start)
}

// Level reads recent samples without changing the WAV or opening another input.
func (r *Recorder) Level() InputLevel {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil || r.meter == nil {
		return InputLevel{}
	}
	return r.meter.read(r.path, time.Now())
}

// Stop ends the recording and returns the WAV path. The caller owns the file.
func (r *Recorder) Stop() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmd, path, done := r.cmd, r.path, r.done
	r.cmd, r.path, r.done = nil, "", nil
	if cmd == nil {
		return "", ErrNotRecording
	}
	if r.meter != nil {
		r.meter.close()
		r.meter = nil
	}
	// SIGINT lets pw-record/parecord finish the WAV header cleanly.
	_ = cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("recording missing: %w", err)
	}
	if info.Size() <= 44 { // a WAV header and nothing else
		os.Remove(path)
		return "", errors.New("no audio captured; check the input device in GNOME Settings › Sound")
	}
	return path, nil
}

// Cancel stops and discards the recording.
func (r *Recorder) Cancel() {
	if path, err := r.Stop(); err == nil {
		os.Remove(path)
	}
}
