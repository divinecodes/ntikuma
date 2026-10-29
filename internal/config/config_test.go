package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSettingsJSONMatchesFrontend(t *testing.T) {
	s := Defaults()
	s.KeepChatsDays = 30
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["keepChatsDays"]) != "30" || string(fields["maxContextChars"]) != "20000" {
		t.Fatalf("frontend cannot read settings fields: %s", b)
	}
	if _, exists := fields["KeepChatsDays"]; exists {
		t.Fatalf("legacy retention key emitted: %s", b)
	}
}

func TestLoadLegacyRetentionKey(t *testing.T) {
	s := Defaults()
	if err := json.Unmarshal([]byte(`{"KeepChatsDays":90}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.KeepChatsDays != 90 {
		t.Fatalf("legacy retention value lost: %+v", s)
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	m, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Get(); !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("wants defaults, got  %+v", got)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ntikuma", "config.json")
	m, _ := Load(path)
	s := m.Get()
	s.DefaultModel = "openai/gpt-5.1"
	s.Fallbacks = []string{"~anthropic/claude-sonnet-latest"}
	s.AttachSelection = true
	s.WebSearch = WebSearch{MaxResults: 4, MaxFetches: 2, MaxToolCalls: 5}
	if err := m.Save(s); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Get(); !reflect.DeepEqual(got, s) {
		t.Fatalf("round trip mismatch:\n want %+v\n got %+v", s, got)
	}
}

func TestModelsDedupes(t *testing.T) {
	s := Settings{DefaultModel: "a/x", Fallbacks: []string{"", "a/x", "b/y", "c/z"}}
	want := []string{"a/x", "b/y", "c/z"}
	if got := s.Models(); !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestLegacySettingsKeepWebDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.json")
	if err := os.WriteFile(path, []byte(`{"defaultModel":"test/model","systemPrompt":"Keep this"}`), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Get().WebSearch != Defaults().WebSearch || m.Get().SystemPrompt != "Keep this" {
		t.Fatalf("legacy defaults: %+v", m.Get())
	}
}

func TestValidateWebLimits(t *testing.T) {
	for _, limits := range []WebSearch{{0, 3, 6}, {26, 3, 6}, {5, 0, 6}, {5, 3, 0}, {5, 3, 31}} {
		s := Defaults()
		s.WebSearch = limits
		if s.Validate() == nil {
			t.Fatalf("accepted invalid limits: %+v", limits)
		}
	}
}

func TestLoadRejectsWebLimitsThatWouldRemoveTheBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte(`{"webSearch":{"maxToolCalls":0}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("loaded a zero web budget, which would omit max_tool_calls")
	}
}

func TestValidate(t *testing.T) {
	s := Defaults()
	s.DefaultModel = ""
	if s.Validate() == nil {
		t.Fatal("empty default model should fail")
	}
	s = Defaults()
	s.Fallbacks = []string{"a", "b", "c", "d"}
	if s.Validate() == nil {
		t.Fatal("four fallbacks should fail")
	}
}

func TestLegacyVoiceSettingsGainProfilesAndPreserveWhisper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, []byte(`{"voice":{"engine":"whisper-server","whisperUrl":"http://localhost:9000","whisperModelPath":"/my/model.bin","language":"auto"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := m.Get().Voice
	if got.WhisperModelPath != "/my/model.bin" || got.Language != "auto" || got.WhisperURL != "http://localhost:9000" || got.Qwen.Threads != 4 || got.Parakeet.Threads != 4 || got.Cohere.Threads != 4 || got.Cohere.Language != "en" {
		t.Fatalf("migration: %+v", got)
	}
	s := m.Get()
	s.Voice.Engine = "parakeet"
	s.Voice.Qwen.ModelDir = "/models/qwen"
	s.Voice.Parakeet = LocalModel{ModelDir: "/models/parakeet", Threads: 8}
	s.Voice.Cohere = CohereModel{ModelDir: "/models/cohere", Threads: 6, Language: "fr"}
	if err := m.Save(s); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil || !reflect.DeepEqual(reloaded.Get(), s) {
		t.Fatalf("profile round trip: %v", err)
	}
}

func TestVoiceSettingsAllowMissingFilesButRejectInvalidConfiguration(t *testing.T) {
	s := Defaults()
	s.Voice.Engine = "qwen3-asr"
	s.Voice.Qwen.ModelDir = "/does/not/exist"
	if err := s.Validate(); err != nil {
		t.Fatal("readiness must be separate from saving:", err)
	}
	for _, threads := range []int{-1, 65} {
		s.Voice.Qwen.Threads = threads
		if s.Validate() == nil {
			t.Fatal("invalid thread count accepted")
		}
	}
	s.Voice.Qwen.Threads = 4
	s.Voice.Engine = "unknown"
	if s.Validate() == nil {
		t.Fatal("unknown engine accepted")
	}
}

func TestCohereSettingsNormalizeAndValidateLanguage(t *testing.T) {
	s := Defaults()
	s.Voice.Engine = "cohere-transcribe"
	s.Voice.Cohere = CohereModel{ModelDir: "/not/downloaded/yet", Language: " FR "}
	normalized := s.Voice.Normalized()
	if normalized.Cohere.Language != "fr" || normalized.Cohere.Threads != 4 {
		t.Fatal(normalized.Cohere)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"auto", "tw", "english", "en fr"} {
		s.Voice.Cohere.Language = code
		if s.Validate() == nil {
			t.Fatalf("accepted unsupported language %q", code)
		}
	}
	s.Voice.Cohere.Language = "en"
	s.Voice.Cohere.Threads = 65
	if s.Validate() == nil {
		t.Fatal("accepted invalid Cohere threads")
	}
}

func TestLocalToolsDefaultsMigrationAndEmptyRootsStayDisabled(t *testing.T) {
	defaults := Defaults()
	if defaults.LocalTools || defaults.AllowShellCommands || !reflect.DeepEqual(defaults.ToolRoots, []string{"~/Documents", "~/Downloads", "~/codes"}) {
		t.Fatal(defaults)
	}
	for _, document := range []string{`{"systemPrompt":"legacy"}`, `{"toolRoots":[]}`, `{"toolRoots":null}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
		manager, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		got := manager.Get()
		if got.LocalTools || got.AllowShellCommands {
			t.Fatal("migration enabled tools")
		}
		if document == `{"systemPrompt":"legacy"}` {
			if !reflect.DeepEqual(got.ToolRoots, defaults.ToolRoots) {
				t.Fatal("legacy roots missing")
			}
		} else if len(got.ToolRoots) != 0 {
			t.Fatal("explicitly empty roots restored broad defaults")
		}
		if err := manager.Save(got); err != nil {
			t.Fatal(err)
		}
		again, err := Load(path)
		if err != nil || !reflect.DeepEqual(again.Get(), got) {
			t.Fatalf("empty/default roots round trip: %v", err)
		}
	}
}

func TestLocalToolRootsAreCopiedAndPersistWithMissingFolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	manager, _ := Load(path)
	s := manager.Get()
	s.LocalTools, s.AllowShellCommands = true, true
	s.ToolRoots = []string{"/does/not/exist/yet", "~/my code"}
	if err := manager.Save(s); err != nil {
		t.Fatal(err)
	}
	s.ToolRoots[0] = "/unapproved-after-save"
	copy := manager.Get()
	copy.ToolRoots[0] = "/unapproved-after-get"
	if got := manager.Get(); got.ToolRoots[0] != "/does/not/exist/yet" {
		t.Fatal("mutable slice changed tool permissions", got.ToolRoots)
	}
	again, err := Load(path)
	if err != nil || !reflect.DeepEqual(again.Get(), manager.Get()) {
		t.Fatalf("tool settings did not persist: %v", err)
	}
	for _, roots := range [][]string{{"relative"}, {""}, {"/has\x00nul"}, {"/" + string([]byte{0xff})}, {"/" + string(make([]byte, 4097))}, make([]string, 33)} {
		invalid := Defaults()
		invalid.ToolRoots = roots
		if invalid.Validate() == nil {
			t.Fatalf("accepted invalid roots %q", roots)
		}
	}
}

func TestContextCaptureAndLocalToolsStartOff(t *testing.T) {
	s := Defaults()
	if s.AttachClipboard || s.AttachSelection || s.LocalTools || s.AllowShellCommands {
		t.Fatalf("defaults capture or run something before the user opts in: %+v", s)
	}
	dir := t.TempDir()
	for raw, want := range map[string]bool{`{"systemPrompt":"Keep this"}`: false, `{"attachClipboard":true}`: true} {
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		m, err := Load(path)
		if err != nil || m.Get().AttachClipboard != want {
			t.Fatalf("%s: attachClipboard %v, err %v", raw, m.Get().AttachClipboard, err)
		}
	}
}
