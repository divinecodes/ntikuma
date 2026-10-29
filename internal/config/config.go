package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

type Settings struct {
	DefaultModel string `json:"defaultModel"`

	Fallbacks []string `json:"fallbacks"`

	SystemPrompt string `json:"systemPrompt"`

	// Context capture defaults. each can still be removed per message
	AttachClipboard bool `json:"attachClipboard"`
	AttachSelection bool `json:"attachSelection"`
	OfferScreenshot bool `json:"offerScreenshot"`

	// MaxContextChars caps bytes per clipboard or selection attachment.
	MaxContextChars int `json:"maxContextChars"`

	Voice              Voice     `json:"voice"`
	WebSearch          WebSearch `json:"webSearch"`
	LocalTools         bool      `json:"localTools"`
	ToolRoots          []string  `json:"toolRoots"`
	AllowShellCommands bool      `json:"allowShellCommands"`

	// keep chat days. delete chats older than this many days. 0 keeps the chats forever
	KeepChatsDays int `json:"keepChatsDays"`

	UsePortalShortcuts bool `json:"usePortalShortcuts"`
}

type WebSearch struct {
	MaxResults   int `json:"maxResults"`
	MaxFetches   int `json:"maxFetches"`
	MaxToolCalls int `json:"maxToolCalls"`
}

type Voice struct {
	// Engine selects the transcription backend.
	Engine string `json:"engine"`

	//is the whisper.cpp server base URL
	WhisperURL string `json:"whisperUrl"`

	// bin is where ntikuma can start the whisper-server
	WhisperServerBin string `json:"whisperServerBin"`
	WhisperModelPath string `json:"whisperModelPath"`

	Language     string      `json:"language"`
	NativeHelper string      `json:"nativeHelper"`
	Qwen         LocalModel  `json:"qwen"`
	Parakeet     LocalModel  `json:"parakeet"`
	Cohere       CohereModel `json:"cohere"`
}

type CohereModel struct {
	ModelDir string `json:"modelDir"`
	Threads  int    `json:"threads"`
	Language string `json:"language"`
}

type LocalModel struct {
	ModelDir string `json:"modelDir"`
	Threads  int    `json:"threads"`
}

func (v Voice) Normalized() Voice {
	if v.Engine == "" {
		v.Engine = "whisper-server"
	}
	if v.Qwen.Threads == 0 {
		v.Qwen.Threads = 4
	}
	if v.Parakeet.Threads == 0 {
		v.Parakeet.Threads = 4
	}
	if v.Cohere.Threads == 0 {
		v.Cohere.Threads = 4
	}
	v.Cohere.Language = strings.ToLower(strings.TrimSpace(v.Cohere.Language))
	if v.Cohere.Language == "" {
		v.Cohere.Language = "en"
	}
	return v
}

func (v Voice) validate() error {
	v = v.Normalized()
	switch v.Engine {
	case "whisper-server", "qwen3-asr", "parakeet", "cohere-transcribe":
	default:
		return errors.New("choose a supported transcription model")
	}
	for _, model := range []LocalModel{v.Qwen, v.Parakeet, {ModelDir: v.Cohere.ModelDir, Threads: v.Cohere.Threads}} {
		if model.Threads < 1 || model.Threads > 64 {
			return errors.New("voice CPU threads must be between 1 and 64")
		}
	}
	if !slices.Contains(strings.Fields("en fr de it es pt el nl pl zh ja ko vi ar"), v.Cohere.Language) {
		return errors.New("choose a supported Cohere transcription language")
	}
	return nil
}

func Defaults() Settings {
	return Settings{
		DefaultModel:    "~anthropic/claude-sonnet-latest",
		Fallbacks:       []string{"openai/gpt-5.1"},
		SystemPrompt:    "You are Ntikuma, a quick desktop assistant. Answer directly and concisely. Use Markdown for code.",
		AttachClipboard: false,
		AttachSelection: false,
		OfferScreenshot: true,
		MaxContextChars: 20000,
		Voice: Voice{
			Engine:     "whisper-server",
			WhisperURL: "http://127.0.0.1:8178",
			Language:   "en",
			Qwen:       LocalModel{Threads: 4},
			Parakeet:   LocalModel{Threads: 4},
			Cohere:     CohereModel{Threads: 4, Language: "en"},
		},
		WebSearch:          WebSearch{MaxResults: 5, MaxFetches: 3, MaxToolCalls: 6},
		ToolRoots:          []string{"~/Documents", "~/Downloads", "~/codes"},
		KeepChatsDays:      0,
		UsePortalShortcuts: false,
	}
}

func (s Settings) Models() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range append([]string{s.DefaultModel}, s.Fallbacks...) {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func (s Settings) Validate() error {
	if s.DefaultModel == "" {
		return errors.New("pick a default model")
	}
	if len(s.Fallbacks) > 3 {
		return errors.New("keep it to three fallbacks at most")
	}

	if s.MaxContextChars < 0 {
		return errors.New("context limit can't be negative")
	}
	if err := s.Voice.validate(); err != nil {
		return err
	}
	if err := validateToolRoots(s.ToolRoots); err != nil {
		return err
	}
	return s.WebSearch.validate()
}

// Validate paths without requiring a removable or not-yet-created folder to
// exist. Canonical containment is checked again when a file tool executes.
func validateToolRoots(roots []string) error {
	if len(roots) > 32 {
		return errors.New("keep at most 32 tool folders")
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" || !utf8.ValidString(root) || strings.IndexByte(root, 0) >= 0 || len(root) > 4096 {
			return errors.New("tool folders need a nonempty UTF-8 path of at most 4096 bytes")
		}
		if !filepath.IsAbs(root) && !strings.HasPrefix(root, "~/") {
			return errors.New("tool folders need an absolute path or ~/ path")
		}
	}
	return nil
}

func (w WebSearch) validate() error {
	if w.MaxResults < 1 || w.MaxResults > 25 {
		return errors.New("web search results must be between 1 and 25")
	}
	if w.MaxFetches < 1 {
		return errors.New("web fetch limit must be positive")
	}
	if w.MaxToolCalls < 1 || w.MaxToolCalls > 30 {
		return errors.New("web tool calls must be between 1 and 30")
	}

	return nil
}

type Paths struct {
	ConfigDir  string
	ConfigFile string
	DataDir    string
	Database   string
	CacheDir   string
}

func DefaultPaths() Paths {
	home, _ := os.UserHomeDir()
	xdg := func(env, fallback string) string {
		if v := os.Getenv(env); v != "" {
			return v
		}

		return filepath.Join(home, fallback)
	}

	config := filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "ntikuma")
	data := filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "ntikuma")
	cache := filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "ntikuma")

	return Paths{
		ConfigDir:  config,
		ConfigFile: filepath.Join(config, "config.json"),
		DataDir:    data,
		Database:   filepath.Join(data, "ntikuma.db"),
		CacheDir:   cache,
	}
}

func (p Paths) Ensure() error {
	for _, d := range []string{p.ConfigDir, p.DataDir, p.CacheDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

type Manager struct {
	path string
	mu   sync.RWMutex
	cur  Settings
}

// Load reads the settingts from path, filling anything missing from Defaults.
func Load(path string) (*Manager, error) {
	m := &Manager{path: path, cur: Defaults()}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}

	//Unmarshal over the defaults so new fields keep their default values
	if err := json.Unmarshal(b, &m.cur); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	m.cur.Voice = m.cur.Voice.Normalized()
	if err := validateToolRoots(m.cur.ToolRoots); err != nil {
		return nil, fmt.Errorf("invalid tool folders in %s: %w", path, err)
	}
	if err := m.cur.WebSearch.validate(); err != nil {
		return nil, fmt.Errorf("invalid web search settings in %s: %w", path, err)
	}
	return m, nil
}

// Get returns a copy of the current settings.
func (m *Manager) Get() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.cur
	s.Fallbacks = append([]string(nil), m.cur.Fallbacks...)
	s.ToolRoots = slices.Clone(m.cur.ToolRoots)
	return s
}

func (m *Manager) Save(s Settings) error {
	s.Fallbacks = slices.Clone(s.Fallbacks)
	s.ToolRoots = slices.Clone(s.ToolRoots)
	s.Voice = s.Voice.Normalized()
	if err := s.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return err
	}
	m.mu.Lock()
	m.cur = s
	m.mu.Unlock()
	return nil
}
