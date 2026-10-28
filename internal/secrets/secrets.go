package secrets

import (
	"errors"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	service = "ntikuma"
	user    = "openrouter-api-key"
)

// EnvVar overrides the keyring for development
const EnvVar = "NTIKUMA_OPENROUTER_KEY"

type Keyring struct{}

// APIKey returns the stored key or "" if none is set
func (Keyring) APIKey() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvVar)); v != "" {
		return v, nil
	}
	v, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return v, err
}

// SetAPIKey stores the key
func (Keyring) SetAPIKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		err := keyring.Delete(service, user)
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}

	return keyring.Set(service, user, key)
}
