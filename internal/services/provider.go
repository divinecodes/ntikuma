package services

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"ntikuma/internal/config"
	"ntikuma/internal/openrouter"
)

// endpoint is where one answer goes, fixed when Send starts: later changes to
// the provider, its address or its key apply to the next question.
type endpoint struct {
	name       string
	connection *config.Connection // nil for OpenRouter
	key        string
}

func newClient(ep endpoint) LLM {
	client := openrouter.New(ep.key)
	if ep.connection != nil {
		client.BaseURL, client.Direct = ep.connection.BaseURL, true
	}
	return client
}

// resolveEndpoint finds the active provider in settings and its key. A direct
// connection's key is looked up for the address in settings, so a key saved
// for another address is never paired with this one.
func (s *ChatService) resolveEndpoint(settings config.Settings) (endpoint, error) {
	conn, err := settings.ActiveConnection()
	if err != nil {
		return endpoint{}, err
	}
	if conn == nil {
		key, err := s.keys.APIKey()
		if err != nil {
			return endpoint{}, fmt.Errorf("couldn't read the keyring: %w", err)
		}
		if key == "" {
			return endpoint{}, errors.New("add your OpenRouter key in Preferences (Ctrl+,)")
		}
		return endpoint{name: "OpenRouter", key: key}, nil
	}
	origin, err := config.Origin(conn.BaseURL)
	if err != nil {
		return endpoint{}, err
	}
	key, err := s.keys.ConnectionKey(conn.ID, origin)
	if err != nil {
		return endpoint{}, fmt.Errorf("couldn't read the keyring: %w", err)
	}
	if key == "" && conn.NeedsKey() {
		return endpoint{}, fmt.Errorf("add your %s key in Preferences (Ctrl+,)", conn.Name)
	}
	return endpoint{name: conn.Name, connection: conn, key: key}, nil
}

func (ep endpoint) friendlyError(err error) string {
	var apiErr *openrouter.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 401:
			return ep.name + " rejected the key. Check it in Preferences"
		case 402:
			return "Out of " + ep.name + " credits"
		case 429:
			return "Every model in the list is rate-limited right now. Try again shortly"
		}
		return apiErr.Message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ep.name + " took too long to respond"
	}
	if ep.connection != nil && config.IsLoopback(ep.connection.BaseURL) && errors.Is(err, syscall.ECONNREFUSED) {
		origin, _ := config.Origin(ep.connection.BaseURL)
		return fmt.Sprintf("Couldn't reach %s at %s. Is it running?", ep.name, origin)
	}
	// The partial text is saved alongside, so say why it stops where it does.
	var finish *openrouter.FinishError
	if errors.As(err, &finish) {
		return sentence(finish.Error())
	}
	if errors.Is(err, openrouter.ErrIncomplete) {
		return sentence(openrouter.ErrIncomplete.Error())
	}
	return err.Error()
}

// fallbackLLM tries a direct provider's models in order, one request each.
// OpenRouter takes the whole list in one request and needs no wrapper.
type fallbackLLM struct{ LLM }

var _ contextSizer = fallbackLLM{}

func (f fallbackLLM) Stream(ctx context.Context, req openrouter.Request, onDelta func(string)) (openrouter.Result, error) {
	var res openrouter.Result
	var err error
	for i, model := range req.Models {
		one := req
		one.Models = []string{model}
		streamed := false
		res, err = f.LLM.Stream(ctx, one, func(delta string) {
			streamed = streamed || delta != ""
			onDelta(delta)
		})
		res.Fallback = res.Fallback || i > 0
		// Stop, or text already shown, ends the answer here.
		if err == nil || ctx.Err() != nil || streamed || !retryable(err) {
			break
		}
	}
	return res, err
}

// ContextLength keeps the wrapped client's lookup, so wrapping it never
// silently falls back to the default history budget.
func (f fallbackLLM) ContextLength(ctx context.Context, models []string) (int, error) {
	if sizer, ok := f.LLM.(contextSizer); ok {
		return sizer.ContextLength(ctx, models)
	}
	return 0, nil
}

// retryable reports an HTTP failure another model might not have: missing,
// timed out, rate-limited, down or overloaded (Anthropic's 529). Errors from
// inside a stream have no status and aren't retried.
func retryable(err error) bool {
	var apiErr *openrouter.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch status := apiErr.Status; {
	case status == 404, status == 408, status == 429, status >= 500:
		return true
	}
	return false
}
