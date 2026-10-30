package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func validConnection() Connection {
	return Connection{ID: "c-0123456789ab", Kind: KindOpenAICompatible, Name: "Ollama", BaseURL: "http://localhost:11434/v1", DefaultModel: "llama3.3", Fallbacks: []string{"qwen3"}, ContextTokens: 4096}
}

func TestConnectionsRoundTripAndPickTheActiveProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, _ := Load(path)
	s := m.Get()
	if s.Provider != "" || len(s.Connections) != 0 {
		t.Fatalf("defaults: %+v", s)
	}
	if c, err := s.ActiveConnection(); c != nil || err != nil {
		t.Fatalf("OpenRouter is the default: %v %v", c, err)
	}
	ollama := validConnection()
	claude := Connection{ID: "c-ba9876543210", Kind: KindAnthropic, Name: "Anthropic", BaseURL: "https://api.anthropic.com", DefaultModel: "claude-sonnet-5-5", ContextTokens: 200000, MaxTokens: 16000}
	s.Connections = []Connection{ollama, claude}
	s.Provider = claude.ID
	if err := m.Save(s); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil || !reflect.DeepEqual(again.Get(), s) {
		t.Fatalf("round trip: %v\n%+v", err, again.Get())
	}
	active, err := again.Get().ActiveConnection()
	if err != nil || active == nil || active.ID != claude.ID {
		t.Fatalf("active: %+v %v", active, err)
	}
	s.Provider = ProviderOpenRouter
	if c, err := s.ActiveConnection(); c != nil || err != nil {
		t.Fatalf("explicit OpenRouter: %v %v", c, err)
	}
}

func TestInvalidConnectionsAreRejected(t *testing.T) {
	cases := map[string]func(*Settings){
		"bad id":              func(s *Settings) { s.Connections[0].ID = "openai" },
		"uppercase id":        func(s *Settings) { s.Connections[0].ID = "c-0123456789AB" },
		"duplicate id":        func(s *Settings) { s.Connections = append(s.Connections, s.Connections[0]) },
		"unknown kind":        func(s *Settings) { s.Connections[0].Kind = "gemini" },
		"empty name":          func(s *Settings) { s.Connections[0].Name = "  " },
		"long name":           func(s *Settings) { s.Connections[0].Name = strings.Repeat("n", 65) },
		"http to the network": func(s *Settings) { s.Connections[0].BaseURL = "http://192.168.1.20:11434/v1" },
		"http to a name":      func(s *Settings) { s.Connections[0].BaseURL = "http://ollama.lan/v1" },
		"ftp":                 func(s *Settings) { s.Connections[0].BaseURL = "ftp://localhost/v1" },
		"no host":             func(s *Settings) { s.Connections[0].BaseURL = "https:///v1" },
		"relative":            func(s *Settings) { s.Connections[0].BaseURL = "localhost:11434" },
		"userinfo":            func(s *Settings) { s.Connections[0].BaseURL = "https://user:pass@api.openai.com/v1" },
		"query":               func(s *Settings) { s.Connections[0].BaseURL = "https://api.openai.com/v1?key=x" },
		"fragment":            func(s *Settings) { s.Connections[0].BaseURL = "https://api.openai.com/v1#x" },
		"four fallbacks":      func(s *Settings) { s.Connections[0].Fallbacks = []string{"a", "b", "c", "d"} },
		"negative context":    func(s *Settings) { s.Connections[0].ContextTokens = -1 },
		"negative max tokens": func(s *Settings) { s.Connections[0].MaxTokens = -1 },
		"nine connections": func(s *Settings) {
			for i := range 8 {
				c := validConnection()
				c.ID = "c-00000000000" + string(rune('0'+i))
				s.Connections = append(s.Connections, c)
			}
		},
		"unknown provider":            func(s *Settings) { s.Provider = "c-ffffffffffff" },
		"active without a model":      func(s *Settings) { s.Provider = s.Connections[0].ID; s.Connections[0].DefaultModel = "" },
		"provider names a model kind": func(s *Settings) { s.Provider = KindAnthropic },
	}
	for name, mutate := range cases {
		s := Defaults()
		s.Connections = []Connection{validConnection()}
		mutate(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, s.Connections)
		}
	}
	for _, base := range []string{"http://localhost:11434/v1", "http://127.0.0.1:1234/v1", "http://127.8.9.10/v1", "http://[::1]:8080", "https://api.openai.com/v1", "https://api.anthropic.com"} {
		s := Defaults()
		c := validConnection()
		c.BaseURL = base
		s.Connections = []Connection{c}
		s.Provider = c.ID
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", base, err)
		}
	}
}

func TestLoadKeepsAnUnknownProviderButRejectsUnsafeConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	// A hand edit or partial write can leave the active ID dangling. Loading
	// keeps it, so sending refuses rather than picking another provider.
	if err := os.WriteFile(path, []byte(`{"provider":"c-ffffffffffff"}`), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := m.Get().ActiveConnection(); c != nil || err == nil {
		t.Fatalf("unknown provider resolved: %v %v", c, err)
	}
	if err := os.WriteFile(path, []byte(`{"connections":[{"id":"c-0123456789ab","kind":"openai-compatible","name":"LAN","baseUrl":"http://10.0.0.2/v1"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("loaded a plain-http connection to another machine")
	}
}

func TestConnectionsAreDeepCopied(t *testing.T) {
	m, _ := Load(filepath.Join(t.TempDir(), "config.json"))
	s := m.Get()
	s.Connections = []Connection{validConnection()}
	if err := m.Save(s); err != nil {
		t.Fatal(err)
	}
	s.Connections[0].Fallbacks[0] = "after-save"
	s.Connections[0].Name = "after-save"
	got := m.Get()
	got.Connections[0].Fallbacks[0] = "after-get"
	got.Connections[0].BaseURL = "https://after-get.example"
	if c := m.Get().Connections[0]; c.Fallbacks[0] != "qwen3" || c.Name != "Ollama" || c.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("stored connection changed through a copy: %+v", c)
	}
}

func TestUpdateReadsAndWritesAtomically(t *testing.T) {
	m, _ := Load(filepath.Join(t.TempDir(), "config.json"))
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Update(func(s Settings) (Settings, error) {
				s.KeepChatsDays++
				return s, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := m.Get().KeepChatsDays; got != 40 {
		t.Fatalf("lost updates: %d of 40", got)
	}
	before := m.Get()
	if _, err := m.Update(func(s Settings) (Settings, error) {
		s.KeepChatsDays = 1
		return s, os.ErrPermission
	}); err == nil || !reflect.DeepEqual(m.Get(), before) {
		t.Fatalf("failed update changed settings: %v", err)
	}
}

func TestOriginAndLoopback(t *testing.T) {
	for base, want := range map[string]string{
		"https://API.OpenAI.com/v1":   "https://api.openai.com",
		"https://api.openai.com:8443": "https://api.openai.com:8443",
		"http://localhost:11434/v1/":  "http://localhost:11434",
		"http://[::1]:8080/v1":        "http://[::1]:8080",
	} {
		if got, err := Origin(base); err != nil || got != want {
			t.Errorf("Origin(%q) = %q, %v; want %q", base, got, err, want)
		}
	}
	for base, want := range map[string]bool{
		"http://localhost:11434/v1": true, "http://127.0.0.1/v1": true, "http://[::1]/": true,
		"https://api.openai.com/v1": false, "http://192.168.0.2/": false, "http://localhost.example.com/": false,
	} {
		if got := IsLoopback(base); got != want {
			t.Errorf("IsLoopback(%q) = %v", base, got)
		}
	}
	c := validConnection()
	if c.NeedsKey() {
		t.Error("a local server needs no key")
	}
	c.BaseURL = "https://api.openai.com/v1"
	if !c.NeedsKey() {
		t.Error("a remote API needs a key")
	}
}

func TestNewConnectionIDsAreUniqueAndValid(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		id, err := NewConnectionID()
		if err != nil || !connectionID.MatchString(id) || seen[id] {
			t.Fatalf("id %q, %v", id, err)
		}
		seen[id] = true
	}
}

func TestPresetsAreValidOnceTheyHaveAnID(t *testing.T) {
	for _, preset := range Presets() {
		if preset.ID != "" {
			t.Fatalf("%s preset carries an ID", preset.Name)
		}
		s := Defaults()
		preset.ID, _ = NewConnectionID()
		s.Connections = []Connection{preset}
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", preset.Name, err)
		}
	}
}

func TestModelChainsComeFromTheActiveProvider(t *testing.T) {
	s := Defaults()
	s.DefaultModel, s.Fallbacks = "openrouter/default", []string{"openrouter/fallback"}
	ollama := validConnection()
	s.Connections = []Connection{ollama}
	chain := func(override string) []string {
		t.Helper()
		got, err := s.ModelChain(override)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := chain(""); !reflect.DeepEqual(got, []string{"openrouter/default", "openrouter/fallback"}) {
		t.Fatalf("OpenRouter: %v", got)
	}
	// OpenRouter keeps accepting any override, followed by its fallbacks.
	if got := chain("vendor/other"); !reflect.DeepEqual(got, []string{"vendor/other", "openrouter/fallback"}) {
		t.Fatalf("OpenRouter override: %v", got)
	}
	s.Provider = ollama.ID
	if got := chain(""); !reflect.DeepEqual(got, []string{"llama3.3", "qwen3"}) || !reflect.DeepEqual(s.Models(), got) {
		t.Fatalf("Ollama: %v %v", got, s.Models())
	}
	if got := chain("qwen3"); !reflect.DeepEqual(got, []string{"qwen3"}) {
		t.Fatalf("Ollama override: %v", got)
	}
	if got := chain("llama3.3"); !reflect.DeepEqual(got, []string{"llama3.3", "qwen3"}) {
		t.Fatalf("Ollama default as override: %v", got)
	}
	// A model from another provider is a stale picker, not a request.
	if _, err := s.ModelChain("openrouter/default"); err == nil {
		t.Fatal("sent another provider's model to Ollama")
	}
	s.Provider = "c-ffffffffffff"
	if _, err := s.ModelChain(""); err == nil || s.Models() != nil {
		t.Fatal("unknown provider has a chain")
	}
}

func TestLocalConnectionsNeedAContextWindowToBeActive(t *testing.T) {
	s := Defaults()
	c := validConnection()
	c.ContextTokens = 0
	s.Connections = []Connection{c}
	if err := s.Validate(); err != nil {
		t.Fatalf("inactive: %v", err)
	}
	s.Provider = c.ID
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "context window") {
		t.Fatalf("active without a window: %v", err)
	}
	s.Connections[0].ContextTokens = 4096
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	// Remote APIs publish large windows; leaving it unknown is fine.
	s.Connections[0].BaseURL, s.Connections[0].ContextTokens = "https://api.openai.com/v1", 0
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, p := range Presets() {
		if IsLoopback(p.BaseURL) && p.ContextTokens == 0 {
			t.Errorf("%s preset has no context window", p.Name)
		}
	}
	var anthropic []Connection
	for _, p := range Presets() {
		if p.Kind == KindAnthropic {
			anthropic = append(anthropic, p)
		}
	}
	if len(anthropic) != 1 || anthropic[0].BaseURL != "https://api.anthropic.com" {
		t.Errorf("Anthropic presets %+v", anthropic)
	}
}
