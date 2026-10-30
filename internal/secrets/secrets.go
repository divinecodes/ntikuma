package secrets

import (
	"encoding/json"
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

func connectionUser(id string) string { return "connection-" + id + "-api-key" }

// boundKey is a direct connection's key and the origin it was entered for.
type boundKey struct {
	Origin string `json:"origin"`
	Key    string `json:"key"`
}

func readConnectionKey(id string) (*boundKey, error) {
	v, err := keyring.Get(service, connectionUser(id))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var bound boundKey
	if json.Unmarshal([]byte(v), &bound) != nil || bound.Origin == "" || bound.Key == "" {
		return nil, errors.New("the saved provider key is unreadable; enter it again in Preferences")
	}
	return &bound, nil
}

// ConnectionKey returns the key for connection id, or "" when none is saved or
// it was saved for a different origin. Changing a connection's address
// therefore stops its key being sent at once, with nothing to clear.
func (Keyring) ConnectionKey(id, origin string) (string, error) {
	bound, err := readConnectionKey(id)
	if err != nil || bound == nil || bound.Origin != origin {
		return "", err
	}
	return bound.Key, nil
}

// SetConnectionKey stores key for connection id, bound to origin. An empty
// key deletes it.
func (k Keyring) SetConnectionKey(id, origin, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return k.DeleteConnectionKey(id)
	}
	if origin == "" {
		return errors.New("a provider key needs the address it's for")
	}
	b, err := json.Marshal(boundKey{Origin: origin, Key: key})
	if err != nil {
		return err
	}
	return keyring.Set(service, connectionUser(id), string(b))
}

// DeleteConnectionKey removes connection id's key. A missing key is fine.
func (Keyring) DeleteConnectionKey(id string) error {
	err := keyring.Delete(service, connectionUser(id))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
