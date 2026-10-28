package services

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ntikuma/internal/config"
)

type settingsKeys struct{ key string }

func (k *settingsKeys) APIKey() (string, error)    { return k.key, nil }
func (k *settingsKeys) SetAPIKey(key string) error { k.key = key; return nil }

func TestPreferencesPersistAllFieldsWithoutAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := &settingsKeys{}
	svc := NewSettingsService(cfg, keys)
	want := config.Settings{
		DefaultModel: "test/model", Fallbacks: nil, SystemPrompt: "Custom prompt",
		AttachClipboard: false, AttachSelection: true, OfferScreenshot: false,
		MaxContextChars: 5000, KeepChatsDays: 90, UsePortalShortcuts: true,
		WebSearch:  config.WebSearch{MaxResults: 4, MaxFetches: 2, MaxToolCalls: 5},
		LocalTools: true, ToolRoots: []string{"~/Documents", "/my/code"}, AllowShellCommands: true,
		Voice: config.Voice{
			Engine: "whisper-server", WhisperURL: "http://127.0.0.1:9000",
			WhisperServerBin: "/opt/whisper-server", WhisperModelPath: "/opt/model.bin", Language: "auto",
			NativeHelper: "/opt/ntikuma-asr",
			Qwen:         config.LocalModel{ModelDir: "/models/qwen", Threads: 4},
			Parakeet:     config.LocalModel{ModelDir: "/models/parakeet", Threads: 6},
			Cohere:       config.CohereModel{ModelDir: "/models/cohere", Threads: 8, Language: "en"},
		},
	}
	if err := svc.SetAPIKey("opaque-test-key"); err != nil {
		t.Fatal(err)
	}
	if !svc.HasAPIKey() {
		t.Fatal("key status did not update")
	}
	if err := svc.Save(want); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get(); !reflect.DeepEqual(got, want) {
		t.Fatalf("preferences did not persist: want %+v, got %+v", want, got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "opaque-test-key") {
		t.Fatal("API key leaked into preferences JSON")
	}
	if err := svc.SetAPIKey(""); err != nil {
		t.Fatal(err)
	}
	if svc.HasAPIKey() {
		t.Fatal("key status did not update after deletion")
	}
}

func TestToolFolderPickerReturnsDraftSelectionWithoutSaving(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, _ := config.Load(filepath.Join(t.TempDir(), "preferences.json"))
	before := cfg.Get()
	var initial string
	selected := "/selected/folder"
	svc := NewSettingsService(cfg, &settingsKeys{}, func(path string) (string, error) { initial = path; return selected, nil })
	got, err := svc.PickToolFolder("~/Downloads")
	if err != nil || got != selected || initial != filepath.Join(home, "Downloads") {
		t.Fatalf("picker: %q %q %v", got, initial, err)
	}
	if !reflect.DeepEqual(before, cfg.Get()) {
		t.Fatal("choosing a folder changed saved permissions")
	}
	selected = ""
	if got, err := svc.PickToolFolder(""); err != nil || got != "" || initial != home {
		t.Fatalf("cancel: %q %v", got, err)
	}
	if _, err := NewSettingsService(cfg, &settingsKeys{}).PickToolFolder(""); err == nil {
		t.Fatal("unavailable picker succeeded")
	}
}
