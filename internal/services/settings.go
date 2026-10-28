package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
}

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

// validates and stores preferences
func (s *SettingsService) Save(settings config.Settings) error { return s.cfg.Save(settings) }

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
