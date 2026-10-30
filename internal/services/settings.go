package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ntikuma/internal/anthropic"
	"ntikuma/internal/config"
	"ntikuma/internal/openrouter"
)

// SettingsService reads and  writes preferences and the API Key
type SettingsService struct {
	cfg  *config.Manager
	keys KeyStore

	mu            sync.Mutex
	models        []openrouter.ModelInfo
	fetchedAt     time.Time
	pickDirectory func(string) (string, error)

	connectionMu     sync.Mutex
	connectionModels map[string]cachedConnectionModels // by connection ID and address
}

type cachedConnectionModels struct {
	models    []openrouter.ModelInfo
	fetchedAt time.Time
}

// maxSuggestedModels bounds the suggestions taken from a provider's list.
const maxSuggestedModels = 1000

// NewSettingsService wires the settings service
func NewSettingsService(cfg *config.Manager, keys KeyStore, pickers ...func(string) (string, error)) *SettingsService {
	s := &SettingsService{cfg: cfg, keys: keys}
	if len(pickers) > 0 {
		s.pickDirectory = pickers[0]
	}
	return s
}

// PickToolFolder returns a selection to the draft; it grants no access and saves
// nothing itself. The popup owns native-dialog focus and concurrency handling.
func (s *SettingsService) PickToolFolder(current string) (string, error) {
	if s.pickDirectory == nil {
		return "", errors.New("folder picker is unavailable")
	}
	home, _ := os.UserHomeDir()
	initial := current
	if strings.HasPrefix(initial, "~/") {
		initial = filepath.Join(home, initial[2:])
	}
	if !filepath.IsAbs(initial) {
		initial = home
	}
	return s.pickDirectory(initial)
}

// returns the current preferences
func (s *SettingsService) Get() config.Settings { return s.cfg.Get() }

// validates and stores preferences. Connections are added and removed only
// by AddConnection and RemoveConnection, so a Preferences window that loaded
// before one of those can't undo it.
func (s *SettingsService) Save(settings config.Settings) error {
	_, err := s.cfg.Update(func(current config.Settings) (config.Settings, error) {
		if !sameConnections(current.Connections, settings.Connections) {
			return config.Settings{}, errConnectionsChanged
		}
		return settings, nil
	})
	return err
}

var errConnectionsChanged = errors.New("providers changed since Preferences opened. Reopen Preferences and try again")

// sameConnections reports whether both lists hold the same IDs, each with the
// same kind. Names, addresses and models may differ.
func sameConnections(current, next []config.Connection) bool {
	if len(current) != len(next) {
		return false
	}
	kinds := make(map[string]string, len(current))
	for _, c := range current {
		kinds[c.ID] = c.Kind
	}
	for _, c := range next {
		kind, ok := kinds[c.ID]
		if !ok || kind != c.Kind {
			return false
		}
		delete(kinds, c.ID)
	}
	return true
}

// ConnectionPresets are starting points for AddConnection.
func (s *SettingsService) ConnectionPresets() []config.Connection { return config.Presets() }

// AddConnection saves c under a new ID, ignoring any ID it carries, and
// returns the updated preferences.
func (s *SettingsService) AddConnection(c config.Connection) (config.Settings, error) {
	id, err := config.NewConnectionID()
	if err != nil {
		return config.Settings{}, err
	}
	c.ID = id
	return s.cfg.Update(func(current config.Settings) (config.Settings, error) {
		current.Connections = append(current.Connections, c)
		return current, nil
	})
}

// RemoveConnection deletes the connection's key, then the connection. If the
// key can't be deleted nothing changes, so no key outlives its connection.
// The active provider can't be removed: questions would have nowhere to go.
func (s *SettingsService) RemoveConnection(id string) (config.Settings, error) {
	if !config.ValidConnectionID(id) {
		return config.Settings{}, errors.New("unknown provider")
	}
	if current := s.cfg.Get(); current.Provider == id {
		return config.Settings{}, errActiveConnection
	}
	if err := s.keys.DeleteConnectionKey(id); err != nil {
		return config.Settings{}, fmt.Errorf("couldn't delete the provider's key, so it wasn't removed: %w", err)
	}
	return s.cfg.Update(func(current config.Settings) (config.Settings, error) {
		if current.Provider == id {
			return config.Settings{}, errActiveConnection
		}
		current.Connections = slices.DeleteFunc(current.Connections, func(c config.Connection) bool { return c.ID == id })
		return current, nil
	})
}

var errActiveConnection = errors.New("switch to another provider before removing this one")

// connectionOrigin is the origin connection id's key belongs to now.
func (s *SettingsService) connectionOrigin(id string) (string, error) {
	for _, c := range s.cfg.Get().Connections {
		if c.ID == id {
			return config.Origin(c.BaseURL)
		}
	}
	return "", errors.New("unknown provider")
}

// SetConnectionKey stores key for the connection's current address. Empty
// deletes it.
func (s *SettingsService) SetConnectionKey(id, key string) error {
	origin, err := s.connectionOrigin(id)
	if err != nil {
		return err
	}
	return s.keys.SetConnectionKey(id, origin, key)
}

// HasConnectionKey reports whether a key is saved for the connection's
// current address. The key itself never goes to the frontend.
func (s *SettingsService) HasConnectionKey(id string) bool {
	origin, err := s.connectionOrigin(id)
	if err != nil {
		return false
	}
	k, err := s.keys.ConnectionKey(id, origin)
	return err == nil && k != ""
}

// SetAPIKey stores the key in the keyring. Empty deletes it
func (s *SettingsService) Defaults() config.Settings { return config.Defaults() }

// HasAPIKey reports whether an OpenRouter key is stored. The key itself never
// goes to the frontend.
func (s *SettingsService) HasAPIKey() bool {
	k, err := s.keys.APIKey()
	return err == nil && k != ""
}
func (s *SettingsService) SetAPIKey(key string) error { return s.keys.SetAPIKey(key) }

// models lists OpenRouter's models for the picker, cached for an hour
func (s *SettingsService) Models() ([]openrouter.ModelInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.models != nil && time.Since(s.fetchedAt) < time.Hour {
		return s.models, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := openrouter.New("").Models(ctx)
	if err != nil {
		return s.models, err //stale list beats none
	}
	s.models, s.fetchedAt = models, time.Now()
	return models, nil
}

// ConnectionModels lists a direct provider's models as suggestions, cached
// for an hour per address. It sends the key saved for that address. A
// failure leaves typing a model ID working.
func (s *SettingsService) ConnectionModels(id string) ([]openrouter.ModelInfo, error) {
	var conn *config.Connection
	for _, c := range s.cfg.Get().Connections {
		if c.ID == id {
			conn = &c
			break
		}
	}
	if conn == nil {
		return nil, errors.New("unknown provider")
	}
	origin, err := config.Origin(conn.BaseURL)
	if err != nil {
		return nil, err
	}
	cacheKey := id + " " + conn.BaseURL
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	if cached, ok := s.connectionModels[cacheKey]; ok && time.Since(cached.fetchedAt) < time.Hour {
		return cached.models, nil
	}
	key, err := s.keys.ConnectionKey(id, origin)
	if err != nil {
		return nil, fmt.Errorf("couldn't read the keyring: %w", err)
	}
	if key == "" && conn.NeedsKey() {
		return nil, fmt.Errorf("add your %s key to list its models", conn.Name)
	}
	var lister interface {
		Models(context.Context) ([]openrouter.ModelInfo, error)
	}
	if conn.Kind == config.KindAnthropic {
		lister = anthropic.New(conn.BaseURL, key)
	} else {
		client := openrouter.New(key)
		client.BaseURL, client.Direct = conn.BaseURL, true
		lister = client
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listed, err := lister.Models(ctx)
	if err != nil {
		return nil, err
	}
	var models []openrouter.ModelInfo
	seen := map[string]bool{}
	for _, m := range listed {
		// The same limits as a typed model name, so any suggestion can be saved.
		if m.ID == "" || len(m.ID) > 256 || !utf8.ValidString(m.ID) || strings.ContainsFunc(m.ID, func(r rune) bool { return r < ' ' }) || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		models = append(models, openrouter.ModelInfo{ID: m.ID, Name: m.Name})
		if len(models) == maxSuggestedModels {
			break
		}
	}
	if s.connectionModels == nil {
		s.connectionModels = make(map[string]cachedConnectionModels)
	}
	s.connectionModels[cacheKey] = cachedConnectionModels{models: models, fetchedAt: time.Now()}
	return models, nil
}

// ProviderStatus describes the active provider. An unknown provider is an
// error, as it is for sending.
func (s *SettingsService) ProviderStatus() (ProviderStatus, error) {
	conn, err := s.cfg.Get().ActiveConnection()
	if err != nil {
		return ProviderStatus{}, err
	}
	if conn == nil {
		return ProviderStatus{ID: config.ProviderOpenRouter, Name: "OpenRouter", NeedsKey: true, HasKey: s.HasAPIKey(), Web: true}, nil
	}
	return ProviderStatus{ID: conn.ID, Name: conn.Name, NeedsKey: conn.NeedsKey(), HasKey: s.HasConnectionKey(conn.ID)}, nil
}
