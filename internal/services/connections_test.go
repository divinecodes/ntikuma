package services

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ntikuma/internal/config"
)

// memoryKeys is a keyring that binds connection keys to an origin, like
// secrets.Keyring, and can fail on request.
type memoryKeys struct {
	mu        sync.Mutex
	key       string
	bound     map[string][2]string // id -> origin, key
	readErr   error
	deleteErr error
	calls     []string
}

func (k *memoryKeys) APIKey() (string, error) { k.mu.Lock(); defer k.mu.Unlock(); return k.key, nil }
func (k *memoryKeys) SetAPIKey(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.key = key
	return nil
}
func (k *memoryKeys) ConnectionKey(id, origin string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.readErr != nil {
		return "", k.readErr
	}
	if b, ok := k.bound[id]; ok && b[0] == origin {
		return b[1], nil
	}
	return "", nil
}
func (k *memoryKeys) SetConnectionKey(id, origin, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.bound == nil {
		k.bound = map[string][2]string{}
	}
	if strings.TrimSpace(key) == "" {
		delete(k.bound, id)
		return nil
	}
	k.bound[id] = [2]string{origin, key}
	return nil
}
func (k *memoryKeys) DeleteConnectionKey(id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, "delete "+id)
	if k.deleteErr != nil {
		return k.deleteErr
	}
	delete(k.bound, id)
	return nil
}

func newSettings(t *testing.T) (*SettingsService, *config.Manager, *memoryKeys) {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	keys := &memoryKeys{}
	return NewSettingsService(cfg, keys), cfg, keys
}

func addOllama(t *testing.T, svc *SettingsService) config.Connection {
	t.Helper()
	s, err := svc.AddConnection(config.Connection{ID: "c-000000000000", Kind: config.KindOpenAICompatible, Name: "Ollama", BaseURL: "http://localhost:11434/v1", DefaultModel: "llama3.3", ContextTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	return s.Connections[len(s.Connections)-1]
}

func TestAddedConnectionsGetFreshIDs(t *testing.T) {
	svc, _, _ := newSettings(t)
	first := addOllama(t, svc)
	second := addOllama(t, svc)
	if first.ID == "c-000000000000" || first.ID == second.ID || !config.ValidConnectionID(first.ID) {
		t.Fatalf("IDs: %q %q", first.ID, second.ID)
	}
	if _, err := svc.AddConnection(config.Connection{Kind: config.KindOpenAICompatible, Name: "LAN", BaseURL: "http://10.0.0.2/v1"}); err == nil {
		t.Fatal("added an unsafe connection")
	}
	if got := svc.Get().Connections; len(got) != 2 {
		t.Fatalf("failed add changed settings: %+v", got)
	}
	if len(svc.ConnectionPresets()) == 0 {
		t.Fatal("no presets")
	}
}

func TestSaveCannotAddRemoveOrRetypeConnections(t *testing.T) {
	svc, _, _ := newSettings(t)
	ollama := addOllama(t, svc)
	stale := svc.Get()
	added := addOllama(t, svc)

	// A Preferences window loaded before the add mustn't drop the new one,
	// and one loaded before a remove mustn't bring it back.
	stale.SystemPrompt = "edited"
	if err := svc.Save(stale); err == nil {
		t.Fatal("stale save dropped a connection")
	}
	current := svc.Get()
	if _, err := svc.RemoveConnection(added.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Save(current); err == nil {
		t.Fatal("stale save restored a removed connection")
	}
	current = svc.Get()
	retyped := current
	retyped.Connections = append([]config.Connection(nil), current.Connections...)
	retyped.Connections[0].Kind = config.KindAnthropic
	if err := svc.Save(retyped); err == nil {
		t.Fatal("changed a connection's kind")
	}
	renumbered := svc.Get()
	renumbered.Connections[0].ID = "c-ffffffffffff"
	if err := svc.Save(renumbered); err == nil {
		t.Fatal("changed a connection's ID")
	}

	edited := svc.Get()
	edited.Connections[0].Name = "Local Ollama"
	edited.Connections[0].Fallbacks = []string{"qwen3"}
	edited.Provider = ollama.ID
	if err := svc.Save(edited); err != nil {
		t.Fatal(err)
	}
	if got := svc.Get(); got.Provider != ollama.ID || got.Connections[0].Name != "Local Ollama" {
		t.Fatalf("edit lost: %+v", got)
	}
}

func TestRemovingAConnectionDeletesItsKeyFirst(t *testing.T) {
	svc, _, keys := newSettings(t)
	ollama := addOllama(t, svc)
	if err := svc.SetConnectionKey(ollama.ID, "fake-test-key"); err != nil {
		t.Fatal(err)
	}

	keys.deleteErr = errors.New("keyring locked")
	if _, err := svc.RemoveConnection(ollama.ID); err == nil {
		t.Fatal("removed a connection whose key couldn't be deleted")
	}
	if len(svc.Get().Connections) != 1 || !svc.HasConnectionKey(ollama.ID) {
		t.Fatal("failed key delete still changed things")
	}
	keys.deleteErr = nil

	s := svc.Get()
	s.Provider = ollama.ID
	if err := svc.Save(s); err != nil {
		t.Fatal(err)
	}
	keys.calls = nil
	if _, err := svc.RemoveConnection(ollama.ID); err == nil || !strings.Contains(err.Error(), "switch to another provider") {
		t.Fatalf("removed the active provider: %v", err)
	}
	if len(keys.calls) != 0 || !svc.HasConnectionKey(ollama.ID) {
		t.Fatal("refused remove still deleted the key")
	}

	s = svc.Get()
	s.Provider = ""
	if err := svc.Save(s); err != nil {
		t.Fatal(err)
	}
	got, err := svc.RemoveConnection(ollama.ID)
	if err != nil || len(got.Connections) != 0 || svc.HasConnectionKey(ollama.ID) {
		t.Fatalf("remove: %+v %v", got, err)
	}
	if _, err := svc.RemoveConnection("../openrouter-api-key"); err == nil {
		t.Fatal("removed a malformed ID")
	}
	if len(keys.calls) != 1 {
		t.Fatalf("keyring calls: %v", keys.calls)
	}
}

func TestConnectionKeysFollowTheConnectionsAddress(t *testing.T) {
	svc, _, _ := newSettings(t)
	ollama := addOllama(t, svc)
	if svc.HasConnectionKey(ollama.ID) {
		t.Fatal("key before saving one")
	}
	if err := svc.SetConnectionKey(ollama.ID, "fake-test-key"); err != nil {
		t.Fatal(err)
	}
	if !svc.HasConnectionKey(ollama.ID) {
		t.Fatal("saved key not found")
	}
	s := svc.Get()
	s.Connections[0].BaseURL = "https://ollama.example.com/v1"
	if err := svc.Save(s); err != nil {
		t.Fatal(err)
	}
	if svc.HasConnectionKey(ollama.ID) {
		t.Fatal("key still offered after the address moved to another origin")
	}
	s.Connections[0].BaseURL = "https://ollama.example.com/api/v1"
	if err := svc.SetConnectionKey(ollama.ID, "fake-test-key"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Save(s); err != nil || !svc.HasConnectionKey(ollama.ID) {
		t.Fatalf("a path change on the same origin lost the key: %v", err)
	}
	if err := svc.SetConnectionKey("c-ffffffffffff", "fake-test-key"); err == nil {
		t.Fatal("saved a key for a missing connection")
	}
}
