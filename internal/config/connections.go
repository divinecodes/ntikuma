package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ProviderOpenRouter is the default provider. An empty Provider means it too.
const ProviderOpenRouter = "openrouter"

// Connection kinds: the wire format a direct connection speaks.
const (
	KindOpenAICompatible = "openai-compatible"
	KindAnthropic        = "anthropic"
)

const maxConnections = 8

// Connection is a provider reached directly instead of through OpenRouter.
type Connection struct {
	// ID is generated once by NewConnectionID and never changes. Its key is
	// stored in the keyring under it.
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Name          string   `json:"name"`
	BaseURL       string   `json:"baseUrl"`
	DefaultModel  string   `json:"defaultModel"`
	Fallbacks     []string `json:"fallbacks"`
	ContextTokens int      `json:"contextTokens"` // 0 when unknown
	MaxTokens     int      `json:"maxTokens"`     // Anthropic answer limit; 0 uses the default
}

// Presets are starting points for new connections; they get an ID when added.
func Presets() []Connection {
	return []Connection{
		{Kind: KindOpenAICompatible, Name: "OpenAI", BaseURL: "https://api.openai.com/v1"},
		{Kind: KindAnthropic, Name: "Anthropic", BaseURL: "https://api.anthropic.com"},
		// Ollama's default window depends on VRAM; 4096 is its smallest.
		{Kind: KindOpenAICompatible, Name: "Ollama", BaseURL: "http://localhost:11434/v1", ContextTokens: 4096},
		{Kind: KindOpenAICompatible, Name: "Kimi", BaseURL: "https://api.moonshot.ai/v1"},
		{Kind: KindOpenAICompatible, Name: "DeepSeek", BaseURL: "https://api.deepseek.com"},
		{Kind: KindOpenAICompatible, Name: "Custom", BaseURL: "http://localhost:8080/v1", ContextTokens: 4096},
	}
}

var connectionID = regexp.MustCompile(`^c-[0-9a-f]{12}$`)

// ValidConnectionID reports whether id has the form NewConnectionID makes.
func ValidConnectionID(id string) bool { return connectionID.MatchString(id) }

// NewConnectionID returns a random ID, so a deleted connection's key and
// state never pass to one added later under the same name.
func NewConnectionID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "c-" + hex.EncodeToString(b), nil
}

// NeedsKey reports whether sending requires an API key. Servers on this
// machine, such as Ollama, usually run without one.
func (c Connection) NeedsKey() bool { return !IsLoopback(c.BaseURL) }

// ErrUnknownProvider means the active provider names no listed connection.
var ErrUnknownProvider = errors.New("the selected provider no longer exists. Choose one in Preferences")

// ActiveConnection returns the connection questions go to, or nil for
// OpenRouter. An ID that matches no connection is an error, never OpenRouter.
func (s Settings) ActiveConnection() (*Connection, error) {
	if s.Provider == "" || s.Provider == ProviderOpenRouter {
		return nil, nil
	}
	for _, c := range s.Connections {
		if c.ID == s.Provider {
			c.Fallbacks = append([]string(nil), c.Fallbacks...)
			return &c, nil
		}
	}
	return nil, ErrUnknownProvider
}

// Origin is the scheme, host and port of a base URL. A key is sent only to
// the origin it was saved for.
func Origin(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("the address needs a scheme and host")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

// IsLoopback reports whether baseURL points at this machine.
func IsLoopback(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateBaseURL(raw string) error {
	if len(raw) > 2048 || !utf8.ValidString(raw) {
		return errors.New("the provider address is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("%q isn't a valid provider address", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("the provider address can't include a user name, query or fragment")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		// Plain HTTP sends questions and keys unencrypted, so only to this machine.
		if !IsLoopback(raw) {
			return errors.New("use https:// for providers on other machines; http:// is only for localhost")
		}
	default:
		return errors.New("the provider address needs https://, or http:// for localhost")
	}
	return nil
}

func validateConnections(connections []Connection) error {
	if len(connections) > maxConnections {
		return fmt.Errorf("keep at most %d provider connections", maxConnections)
	}
	seen := map[string]bool{}
	for _, c := range connections {
		if !connectionID.MatchString(c.ID) || seen[c.ID] {
			return fmt.Errorf("provider connection ID %q is missing, malformed or repeated", c.ID)
		}
		seen[c.ID] = true
		if c.Kind != KindOpenAICompatible && c.Kind != KindAnthropic {
			return fmt.Errorf("unsupported provider type %q", c.Kind)
		}
		if name := strings.TrimSpace(c.Name); name == "" || utf8.RuneCountInString(name) > 64 || !utf8.ValidString(name) {
			return errors.New("give each provider a name of at most 64 characters")
		}
		if err := validateBaseURL(c.BaseURL); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
		if len(c.Fallbacks) > 3 {
			return fmt.Errorf("%s: keep it to three fallbacks at most", c.Name)
		}
		for _, model := range append([]string{c.DefaultModel}, c.Fallbacks...) {
			if len(model) > 256 || !utf8.ValidString(model) || strings.ContainsFunc(model, func(r rune) bool { return r < ' ' }) {
				return fmt.Errorf("%s: model names must be at most 256 characters", c.Name)
			}
		}
		if c.ContextTokens < 0 || c.ContextTokens > 100_000_000 || c.MaxTokens < 0 || c.MaxTokens > 10_000_000 {
			return fmt.Errorf("%s: token limits must be positive numbers", c.Name)
		}
	}
	return nil
}

// validateProvider checks the active provider when saving. Loading skips it,
// so a dangling ID reaches ActiveConnection and sending refuses.
func (s Settings) validateProvider() error {
	c, err := s.ActiveConnection()
	if err != nil {
		return err
	}
	if c != nil && strings.TrimSpace(c.DefaultModel) == "" {
		return fmt.Errorf("pick a default model for %s", c.Name)
	}
	// Local servers often run small windows and drop the start of a prompt
	// that doesn't fit, so the history budget needs the real size.
	if c != nil && IsLoopback(c.BaseURL) && c.ContextTokens == 0 {
		return fmt.Errorf("set the context window for %s", c.Name)
	}
	return nil
}

func cloneConnections(connections []Connection) []Connection {
	if connections == nil {
		return nil
	}
	out := make([]Connection, len(connections))
	for i, c := range connections {
		c.Fallbacks = append([]string(nil), c.Fallbacks...)
		out[i] = c
	}
	return out
}
