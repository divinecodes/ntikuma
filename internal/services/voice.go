package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ntikuma/internal/config"
	"ntikuma/internal/voice"
)

type audioRecorder interface {
	Start() error
	Stop() (string, error)
	Cancel()
	Recording() (bool, time.Duration)
	Level() voice.InputLevel
}

type ownedTranscriber interface {
	voice.Transcriber
	Close()
}

// VoiceService owns one recording or transcription at a time.
type VoiceService struct {
	cfg        *config.Manager
	recorder   audioRecorder
	transcribe func(context.Context, string) (string, error)

	mu             sync.Mutex
	phase          string
	closed         bool
	cancel         context.CancelFunc
	done           chan struct{}
	backend        ownedTranscriber
	wcfg           config.Voice
	newTranscriber func(config.Voice) (ownedTranscriber, error)
	pickDirectory  func(string) (string, error)
	events         Emitter
	levelCancel    context.CancelFunc
	levelDone      chan struct{}
}

func NewVoiceService(cfg *config.Manager, tmpDir string, events Emitter, pickers ...func(string) (string, error)) *VoiceService {
	v := &VoiceService{cfg: cfg, recorder: voice.NewRecorder(tmpDir), events: events}
	if len(pickers) > 0 {
		v.pickDirectory = pickers[0]
	}
	v.newTranscriber = newVoiceTranscriber
	v.transcribe = func(ctx context.Context, path string) (string, error) {
		v.mu.Lock()
		backend := v.backend
		v.mu.Unlock()
		return backend.Transcribe(ctx, path)
	}
	return v
}

func (v *VoiceService) Start(recordingID string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return errors.New("voice service is shutting down")
	}
	if v.phase != "" {
		return errors.New("dictation is already recording or transcribing")
	}
	if strings.TrimSpace(recordingID) == "" {
		return errors.New("recording ID is required")
	}
	// Select before recording starts. Later preference edits belong to the next
	// recording, and cannot unload a model used by this one.
	selected := v.cfg.Get().Voice.Normalized()
	if v.backend == nil || !sameVoiceModel(selected, v.wcfg) {
		backend, err := v.newTranscriber(selected)
		if err != nil {
			return err
		}
		if v.backend != nil {
			v.backend.Close()
		}
		v.backend, v.wcfg = backend, selected
	}
	if err := v.recorder.Start(); err != nil {
		return err
	}
	v.phase = "recording"
	if v.events != nil {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		v.levelCancel, v.levelDone = cancel, done
		go v.monitorLevels(ctx, done, recordingID)
	}
	return nil
}

func sameVoiceModel(a, b config.Voice) bool {
	if a.Engine != b.Engine {
		return false
	}
	if a.Engine != "whisper-server" {
		return localOptions(a) == localOptions(b)
	}
	return a.WhisperURL == b.WhisperURL && a.WhisperServerBin == b.WhisperServerBin && a.WhisperModelPath == b.WhisperModelPath && a.Language == b.Language
}

func (v *VoiceService) monitorLevels(ctx context.Context, done chan struct{}, recordingID string) {
	defer close(done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			level := v.recorder.Level()
			if ctx.Err() != nil {
				return
			}
			v.events.Emit(EventVoiceLevel, VoiceLevelEvent{RecordingID: recordingID, Level: level.Amplitude, Available: level.Available})
		}
	}
}

// Caller holds mu. Join after unlocking so emission never waits on this lock.
func (v *VoiceService) stopLevels() <-chan struct{} {
	if v.levelCancel != nil {
		v.levelCancel()
	}
	done := v.levelDone
	v.levelCancel, v.levelDone = nil, nil
	return done
}

func waitLevels(done <-chan struct{}) {
	if done != nil {
		<-done
	}
}

func (v *VoiceService) Recording() (bool, float64) {
	on, duration := v.recorder.Recording()
	return on, duration.Seconds()
}

func (v *VoiceService) ModelName() string {
	v.mu.Lock()
	selected := v.wcfg
	if v.phase == "" {
		selected = v.cfg.Get().Voice
	}
	v.mu.Unlock()
	switch selected.Engine {
	case "qwen3-asr":
		return "Qwen3-ASR"
	case "parakeet":
		return "Parakeet"
	case "cohere-transcribe":
		return "Cohere Transcribe"
	default:
		return "Whisper"
	}
}

func (v *VoiceService) Stop() (string, error) {
	v.mu.Lock()
	if v.phase != "recording" {
		v.mu.Unlock()
		return "", voice.ErrNotRecording
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	done := make(chan struct{})
	v.phase, v.cancel, v.done = "transcribing", cancel, done
	levels := v.stopLevels()
	v.mu.Unlock()
	waitLevels(levels)
	defer func() {
		cancel()
		v.mu.Lock()
		v.phase, v.cancel, v.done = "", nil, nil
		close(done)
		v.mu.Unlock()
	}()

	path, err := v.recorder.Stop()
	if err != nil {
		return "", err
	}
	defer os.Remove(path)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	text, err := v.transcribe(ctx, path)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("didn't catch anything; try again a little closer to the mic")
	}
	return text, nil
}

// Cancel waits for recording/transcription cleanup before another session starts.
func (v *VoiceService) Cancel() {
	v.mu.Lock()
	closeBackend := v.phase != "" || v.closed
	levels := v.stopLevels()
	if v.phase == "recording" {
		v.recorder.Cancel()
		v.phase = ""
	}
	if v.cancel != nil {
		v.cancel()
	}
	done, backend := v.done, v.backend
	v.mu.Unlock()
	waitLevels(levels)
	if backend != nil && closeBackend {
		backend.Close()
	}
	if done != nil {
		<-done
	}
}

func (v *VoiceService) ServiceShutdown() error {
	v.mu.Lock()
	v.closed = true
	v.mu.Unlock()
	v.Cancel()
	return nil
}

func newVoiceTranscriber(s config.Voice) (ownedTranscriber, error) {
	s = s.Normalized()
	switch s.Engine {
	case "whisper-server":
		w := voice.NewWhisperServer(s.WhisperURL, s.Language)
		w.Bin, w.ModelPath = s.WhisperServerBin, s.WhisperModelPath
		return w, nil
	case "qwen3-asr", "parakeet", "cohere-transcribe":
		options := localOptions(s)
		if _, _, err := voice.LocalPaths(options); err != nil {
			return nil, err
		}
		return &voice.LocalTranscriber{Options: options}, nil
	default:
		return nil, fmt.Errorf("unsupported transcription model %q", s.Engine)
	}
}

func localOptions(settings config.Voice) voice.LocalOptions {
	settings = settings.Normalized()
	if settings.Engine == "cohere-transcribe" {
		return voice.LocalOptions{Engine: settings.Engine, ModelDir: settings.Cohere.ModelDir, Threads: settings.Cohere.Threads, Language: settings.Cohere.Language, HelperPath: settings.NativeHelper}
	}
	model := settings.Qwen
	if settings.Engine == "parakeet" {
		model = settings.Parakeet
	}
	return voice.LocalOptions{Engine: settings.Engine, ModelDir: model.ModelDir, Threads: model.Threads, HelperPath: settings.NativeHelper}
}

type VoiceReadiness struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message"`
}

// CheckSetup checks the supplied draft without saving it, loading weights, or
// opening a microphone. Full model compatibility is checked during loading.
func (v *VoiceService) CheckSetup(settings config.Voice) VoiceReadiness {
	settings = settings.Normalized()
	if settings.Engine == "whisper-server" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server := voice.NewWhisperServer(settings.WhisperURL, settings.Language)
		server.Bin, server.ModelPath = settings.WhisperServerBin, settings.WhisperModelPath
		message, err := server.CheckSetup(ctx)
		if err != nil {
			return VoiceReadiness{Message: err.Error()}
		}
		return VoiceReadiness{Ready: true, Message: message}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := voice.CheckLocal(ctx, localOptions(settings)); err != nil {
		return VoiceReadiness{Message: err.Error()}
	}
	return VoiceReadiness{Ready: true, Message: "Model files and speech runtime are available. The model loads when transcription starts."}
}

func (v *VoiceService) PickModelFolder(engine, current string) (string, error) {
	if len(voice.ModelFiles(engine)) == 0 {
		return "", errors.New("choose a local transcription model first")
	}
	if v.pickDirectory == nil {
		return "", errors.New("folder picker is unavailable")
	}
	initial := current
	if initial == "" {
		folder := "sherpa-onnx-qwen3-asr-0.6B-int8-2026-03-25"
		if engine == "parakeet" {
			folder = "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8"
		}
		if engine == "cohere-transcribe" {
			folder = "sherpa-onnx-cohere-transcribe-14-lang-int8-2026-04-01"
		}
		cwd, _ := os.Getwd()
		candidate := filepath.Join(cwd, "build", "asr-eval", "models", folder)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			initial = candidate
		}
	}
	return v.pickDirectory(initial)
}
