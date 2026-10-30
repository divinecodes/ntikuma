package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ntikuma/internal/config"
	"ntikuma/internal/openrouter"
	"ntikuma/internal/store"
)

func chatWithConfig(t *testing.T, document string, keys KeyStore) (*ChatService, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := NewChatService(st, cfg, keys, &recorder{done: make(chan any, 1)})
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	svc.newLLM = func(string) LLM {
		t.Error("a client was built")
		return streamFunc(func(context.Context, openrouter.Request, func(string)) (openrouter.Result, error) {
			return openrouter.Result{}, errors.New("sent")
		})
	}
	return svc, st
}

const ollamaConnection = `{"id":"c-0123456789ab","kind":"openai-compatible","name":"Ollama","baseUrl":"http://localhost:11434/v1","defaultModel":"llama3.3"}`

func TestSendNeverFallsBackToOpenRouterFromAnotherProvider(t *testing.T) {
	keys := &memoryKeys{key: "fake-openrouter-key"}
	for name, document := range map[string]string{
		"unknown provider": `{"provider":"c-ffffffffffff"}`,
		"direct provider":  `{"provider":"c-0123456789ab","connections":[` + ollamaConnection + `]}`,
	} {
		svc, st := chatWithConfig(t, document, keys)
		_, err := svc.Send(SendRequest{Text: "private question"})
		if err == nil {
			t.Fatalf("%s: sent", name)
		}
		if name == "unknown provider" && !errors.Is(err, config.ErrUnknownProvider) {
			t.Fatalf("%s: %v", name, err)
		}
		if chats, _ := st.ListChats(context.Background(), 10); len(chats) != 0 {
			t.Fatalf("%s: stored a chat", name)
		}
	}
}

func TestEndpointKeyMatchesTheSnapshotsAddress(t *testing.T) {
	keys := &memoryKeys{key: "fake-openrouter-key"}
	svc, _ := chatWithConfig(t, `{}`, keys)
	openai := config.Connection{ID: "c-0123456789ab", Kind: config.KindOpenAICompatible, Name: "OpenAI", BaseURL: "https://api.openai.com/v1", DefaultModel: "m"}
	settings := config.Defaults()
	settings.Connections = []config.Connection{openai}

	ep, err := svc.resolveEndpoint(settings)
	if err != nil || ep.connection != nil || ep.key != "fake-openrouter-key" || ep.name != "OpenRouter" {
		t.Fatalf("OpenRouter: %+v %v", ep, err)
	}

	settings.Provider = openai.ID
	if _, err := svc.resolveEndpoint(settings); err == nil || !strings.Contains(err.Error(), "add your OpenAI key") {
		t.Fatalf("no key: %v", err)
	}
	_ = keys.SetConnectionKey(openai.ID, "https://api.openai.com", "fake-openai-key")
	ep, err = svc.resolveEndpoint(settings)
	if err != nil || ep.connection == nil || ep.connection.ID != openai.ID || ep.key != "fake-openai-key" {
		t.Fatalf("OpenAI: %+v %v", ep, err)
	}
	// The snapshot's address decides, so a key saved for the old address is
	// never paired with a new one.
	settings.Connections[0].BaseURL = "https://proxy.example.com/v1"
	if _, err := svc.resolveEndpoint(settings); err == nil {
		t.Fatal("old address's key sent to a new address")
	}

	// A local server runs without a key, but a keyring failure still stops it.
	settings.Connections[0].BaseURL = "http://localhost:11434/v1"
	if ep, err := svc.resolveEndpoint(settings); err != nil || ep.key != "" {
		t.Fatalf("keyless local server: %+v %v", ep, err)
	}
	keys.readErr = errors.New("keyring locked")
	if _, err := svc.resolveEndpoint(settings); err == nil {
		t.Fatal("keyring error treated as no key needed")
	}

	keys.readErr, keys.key = nil, ""
	settings.Provider = ""
	if _, err := svc.resolveEndpoint(settings); err == nil || !strings.Contains(err.Error(), "OpenRouter key") {
		t.Fatalf("missing OpenRouter key: %v", err)
	}
}
