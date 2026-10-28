package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ntikuma/internal/config"
	"ntikuma/internal/voice"
)

type testRecorder struct {
	mu     sync.Mutex
	path   string
	active bool
}

type levelEvents struct{ levels chan VoiceLevelEvent }

func (e levelEvents) Emit(name string, data ...any) bool {
	if name == EventVoiceLevel {
		e.levels <- data[0].(VoiceLevelEvent)
	}
	return true
}

func waitLevel(t *testing.T, ch <-chan VoiceLevelEvent) VoiceLevelEvent {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("no microphone level event")
		return VoiceLevelEvent{}
	}
}

func TestVoiceLevelsAreThrottledAndStoppedBeforeTranscription(t *testing.T) {
	svc, _ := testVoice(t)
	ch := make(chan VoiceLevelEvent, 64)
	svc.events = levelEvents{ch}
	started := time.Now()
	if err := svc.Start("current-recording"); err != nil {
		t.Fatal(err)
	}
	first, second := waitLevel(t, ch), waitLevel(t, ch)
	if time.Since(started) < 100*time.Millisecond {
		t.Fatal("meter exceeded ten events per second")
	}
	for _, event := range []VoiceLevelEvent{first, second} {
		if event.RecordingID != "current-recording" || event.Level != 0.25 || !event.Available {
			t.Fatalf("event %+v", event)
		}
	}
	done := svc.levelDone
	svc.transcribe = func(context.Context, string) (string, error) {
		select {
		case <-done:
		default:
			t.Error("meter still running during transcription")
		}
		return "Question", nil
	}
	if _, err := svc.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("Stop did not join meter")
	}
	if svc.levelDone != nil || svc.levelCancel != nil {
		t.Fatal("Stop retained meter state")
	}
}

func TestVoiceRepeatedCancellationAndShutdownJoinLevels(t *testing.T) {
	svc, _ := testVoice(t)
	ch := make(chan VoiceLevelEvent, 64)
	svc.events = levelEvents{ch}
	for i := 0; i < 3; i++ {
		if err := svc.Start("repeated-recording"); err != nil {
			t.Fatal(err)
		}
		waitLevel(t, ch)
		done := svc.levelDone
		svc.Cancel()
		select {
		case <-done:
		default:
			t.Fatal("Cancel leaked level collection")
		}
	}
	if err := svc.Start("shutdown-recording"); err != nil {
		t.Fatal(err)
	}
	done := svc.levelDone
	if err := svc.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("shutdown leaked level collection")
	}
}

func TestVoiceLevelJSONAndRequiredRecordingID(t *testing.T) {
	b, err := json.Marshal(VoiceLevelEvent{RecordingID: "recording", Available: true, Level: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["recordingId"] != "recording" || fields["level"] != 0.5 || fields["available"] != true {
		t.Fatalf("frontend contract: %s", b)
	}
	svc, _ := testVoice(t)
	if err := svc.Start(""); err == nil {
		t.Fatal("started without a recording ID")
	}
}

func (r *testRecorder) Start() error { r.mu.Lock(); defer r.mu.Unlock(); r.active = true; return nil }
func (r *testRecorder) Stop() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = false
	return r.path, nil
}
func (r *testRecorder) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = false
	os.Remove(r.path)
}
func (r *testRecorder) Recording() (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active, time.Second
}

func (r *testRecorder) Level() voice.InputLevel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return voice.InputLevel{Amplitude: 0.25, Available: r.active}
}

func testVoice(t *testing.T) (*VoiceService, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "recording.wav")
	if err := os.WriteFile(path, []byte("test audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewVoiceService(cfg, dir, nil)
	svc.recorder = &testRecorder{path: path}
	t.Cleanup(func() { svc.ServiceShutdown() })
	return svc, path
}

func TestVoiceTranscriptCleanupAndSilence(t *testing.T) {
	for _, text := range []string{"  transcript  ", ""} {
		t.Run(text, func(t *testing.T) {
			svc, path := testVoice(t)
			svc.transcribe = func(context.Context, string) (string, error) { return text, nil }
			if err := svc.Start("test-recording"); err != nil {
				t.Fatal(err)
			}
			if err := svc.Start("test-recording"); err == nil {
				t.Fatal("overlapping Start succeeded")
			}
			got, err := svc.Stop()
			if text == "" {
				if err == nil || !strings.Contains(err.Error(), "didn't catch") {
					t.Fatalf("got %v", err)
				}
			} else if err != nil || got != "transcript" {
				t.Fatalf("got %q, %v", got, err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("temporary audio was not removed")
			}
		})
	}
}

func TestVoiceCancelTranscriptionWaitsForCleanup(t *testing.T) {
	svc, path := testVoice(t)
	started := make(chan struct{})
	svc.transcribe = func(ctx context.Context, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	if err := svc.Start("test-recording"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := svc.Stop(); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("transcription did not start")
	}
	if err := svc.Start("test-recording"); err == nil {
		t.Fatal("recording overlapped transcription")
	}
	svc.Cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled audio was not removed")
	}
	if err := svc.Start("test-recording"); err != nil {
		t.Fatal("could not start after cancellation:", err)
	}
}

func TestVoiceShutdownDiscardsRecordingAndRejectsRestart(t *testing.T) {
	svc, path := testVoice(t)
	if err := svc.Start("test-recording"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("shutdown left temporary audio")
	}
	if err := svc.Start("test-recording"); err == nil {
		t.Fatal("Start succeeded after shutdown")
	}
}

type modelTranscriber struct {
	name   string
	closed atomic.Int32
}

func (m *modelTranscriber) Transcribe(context.Context, string) (string, error) { return m.name, nil }
func (m *modelTranscriber) Close()                                             { m.closed.Add(1) }

func TestVoiceSnapshotsModelAtStartAndOnlyReplacesTheActiveConfiguration(t *testing.T) {
	svc, path := testVoice(t)
	var created []*modelTranscriber
	svc.newTranscriber = func(settings config.Voice) (ownedTranscriber, error) {
		backend := &modelTranscriber{name: settings.Engine}
		created = append(created, backend)
		return backend, nil
	}
	s := svc.cfg.Get()
	s.Voice.Engine = "qwen3-asr"
	if err := svc.cfg.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start("first"); err != nil {
		t.Fatal(err)
	}
	s.Voice.Engine = "parakeet"
	if err := svc.cfg.Save(s); err != nil {
		t.Fatal(err)
	}
	if svc.ModelName() != "Qwen3-ASR" {
		t.Fatal("preference change replaced the active model label")
	}
	if got, err := svc.Stop(); err != nil || got != "qwen3-asr" {
		t.Fatalf("settings changed an active recording: %q %v", got, err)
	}
	if created[0].closed.Load() != 0 {
		t.Fatal("successful transcription unloaded its model")
	}
	if err := os.WriteFile(path, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start("second"); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Stop(); err != nil || got != "parakeet" {
		t.Fatalf("next recording did not switch: %q %v", got, err)
	}
	if created[0].closed.Load() != 1 {
		t.Fatal("replaced model was not closed")
	}
	s.Voice.Qwen.ModelDir = "/different/inactive/model"
	if err := svc.cfg.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start("third"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatal("inactive settings caused a model reload")
	}
}

func TestVoiceMissingLocalModelFailsBeforeOpeningMicrophone(t *testing.T) {
	svc, _ := testVoice(t)
	s := svc.cfg.Get()
	s.Voice.Engine = "parakeet"
	if err := svc.cfg.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start("missing-model"); err == nil || !strings.Contains(err.Error(), "model folder") {
		t.Fatalf("missing model: %v", err)
	}
	if active, _ := svc.Recording(); active {
		t.Fatal("microphone opened without a usable model")
	}
}

func TestCohereLanguageChangesReplaceOnlyItsActiveModel(t *testing.T) {
	a := config.Defaults().Voice
	a.Engine = "cohere-transcribe"
	a.Cohere.ModelDir = "/models/cohere"
	b := a
	b.Cohere.Language = "fr"
	if sameVoiceModel(a, b) {
		t.Fatal("language change would reuse the old recognizer")
	}
	options := localOptions(b)
	if options.Language != "fr" || options.ModelDir != "/models/cohere" || options.Threads != 4 {
		t.Fatal(options)
	}
	a.Engine, b.Engine = "parakeet", "parakeet"
	if !sameVoiceModel(a, b) {
		t.Fatal("inactive Cohere language would reload Parakeet")
	}
	svc, _ := testVoice(t)
	s := svc.cfg.Get()
	s.Voice = a
	s.Voice.Engine = "cohere-transcribe"
	if err := svc.cfg.Save(s); err != nil {
		t.Fatal(err)
	}
	if svc.ModelName() != "Cohere Transcribe" {
		t.Fatal(svc.ModelName())
	}
}
