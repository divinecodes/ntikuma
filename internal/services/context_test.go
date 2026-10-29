package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ntikuma/internal/screenshot"
	"ntikuma/internal/selection"
)

type fakeClipboard struct {
	text string
	ok   bool
}

type fakeSelection struct {
	support selection.Support
	read    func(context.Context) (string, error)
}

func (f fakeSelection) Support() selection.Support               { return f.support }
func (f fakeSelection) Read(ctx context.Context) (string, error) { return f.read(ctx) }

func TestSelectionUsesPrimaryWithoutSubstitutingClipboard(t *testing.T) {
	for _, text := range []string{"selected text\n", "a\nb", "  \n"} {
		primary := fakeSelection{support: selection.Support{Available: true}, read: func(context.Context) (string, error) { return text, nil }}
		c := NewContextService(nil, fakeClipboard{"unrelated copied text", true}, nil, primary)
		result, err := c.Selection(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(text) == "" {
			if result.Attachment != nil || result.Message == "" {
				t.Fatalf("empty result: %+v", result)
			}
			continue
		}
		if result.Attachment == nil || result.Attachment.Kind != "selection" || result.Attachment.Content != strings.TrimRight(text, "\n") {
			t.Fatalf("selection lost or clipboard substituted: %+v", result)
		}
	}
}

func TestSelectionUnavailableFailuresAndTimeouts(t *testing.T) {
	primary := fakeSelection{support: selection.Support{Message: "unsupported compositor"}, read: func(context.Context) (string, error) { t.Fatal("read unsupported selection"); return "", nil }}
	c := NewContextService(nil, fakeClipboard{"copied", true}, nil, primary)
	result, err := c.Selection(context.Background())
	if err != nil || result.Attachment != nil || result.Message != "unsupported compositor" {
		t.Fatalf("unsupported: %+v, %v", result, err)
	}
	primary.support.Available = true
	primary.read = func(context.Context) (string, error) { return "", errors.New("owner disappeared") }
	c.selection = primary
	if _, err := c.Selection(context.Background()); err == nil {
		t.Fatal("lost transfer failure")
	}
	primary.read = func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	c.selection = primary
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := c.Selection(ctx); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("timeout: %v", err)
	}
}

func (f fakeClipboard) Text() (string, bool) { return f.text, f.ok }

func TestClipboardAttachment(t *testing.T) {
	c := NewContextService(nil, fakeClipboard{"a\nb\nc\n", true}, nil, nil)
	a, err := c.Clipboard()
	if err != nil || a == nil || a.Label != "Clipboard · 3 lines" || a.Content != "a\nb\nc" {
		t.Fatalf("got %+v, %v", a, err)
	}
	for _, empty := range []fakeClipboard{{"", false}, {"   \n", true}} {
		if a, _ := NewContextService(nil, empty, nil, nil).Clipboard(); a != nil {
			t.Fatalf("empty clipboard gave %+v", a)
		}
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]string{"x": "Clipboard · 1 character", "hello": "Clipboard · 5 characters", "a\nb": "Clipboard · 2 lines"}
	for in, want := range cases {
		if got := describe("Clipboard", in); got != want {
			t.Errorf("describe(%q)=%q want %q", in, got, want)
		}
	}
}

func TestScreenshotAttachmentAndCancellation(t *testing.T) {
	capture := func(context.Context) (screenshot.Image, error) {
		return screenshot.Image{DataURL: "data:image/png;base64,image", Width: 640, Height: 480}, nil
	}
	c := NewContextService(nil, fakeClipboard{}, capture, nil)
	a, err := c.Screenshot(context.Background())
	if err != nil || a == nil || a.Kind != "screenshot" || a.Label != "Screenshot · 640 × 480" || a.Content != "data:image/png;base64,image" {
		t.Fatalf("attachment: %+v, %v", a, err)
	}
	for _, captureErr := range []error{screenshot.ErrCancelled, context.Canceled, errors.New("permission denied")} {
		c.capture = func(context.Context) (screenshot.Image, error) { return screenshot.Image{}, captureErr }
		a, err := c.Screenshot(context.Background())
		if a != nil || (captureErr.Error() == "permission denied" && err == nil) || (captureErr.Error() != "permission denied" && err != nil) {
			t.Fatalf("capture error %v: %+v, %v", captureErr, a, err)
		}
	}
}

func TestScreenshotCancelBusyAndShutdown(t *testing.T) {
	started, done := make(chan struct{}), make(chan error, 1)
	c := NewContextService(nil, fakeClipboard{}, func(ctx context.Context) (screenshot.Image, error) {
		close(started)
		<-ctx.Done()
		return screenshot.Image{}, ctx.Err()
	}, nil)
	go func() { _, err := c.Screenshot(context.Background()); done <- err }()
	<-started
	if _, err := c.Screenshot(context.Background()); err == nil {
		t.Fatal("allowed two capture dialogs")
	}
	c.CancelScreenshot()
	if err := <-done; err != nil {
		t.Fatalf("cancel should leave composer usable: %v", err)
	}
	_ = c.ServiceShutdown()
	if _, err := c.Screenshot(context.Background()); err == nil {
		t.Fatal("capture started after shutdown")
	}
}

func TestLooksSecretFlagsKeysTokensAndPasswords(t *testing.T) {
	// Fake tokens are split so the CI secrets scan doesn't flag them.
	secrets := []string{
		"sk-or-v1-0123456789abcdef0123456789abcdef0123456789abcdef",
		"  ghp_" + "16C7e42F292c6912E7710c838347Ae178B4a\n",
		"AKIAIOSFODNN7EXAMPLE",
		"xoxb-" + "123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx",
		"eyJhbGciOiJIUzI1NiJ9" + ".eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----",
		"Tr0ub4dor&3",
		"k9#Lm2$pQx7!vR4w",
		"Summer2024!",
		"Xq7mP2vL9kR4tW8z",
		"3d3c42e5aac5ba805825da76410c181273ba90b1",
		"DATABASE_PASSWORD=hunter2hunter2",
		"export OPENAI_API_KEY='sk-proj-abcdefghijklmnopqrstuvwx'",
		"curl https://openrouter.ai/api/v1/models -H \"Authorization: Bearer sk-or-v1-0123456789abcdef0123456789\"",
		"host: db\npassword: correct-horse-battery\nport: 5432",
	}
	for _, text := range secrets {
		if !looksSecret(text) {
			t.Errorf("missed secret %q", text)
		}
	}
	ordinary := []string{
		"What does this error mean?",
		"https://example.com/docs/getting-started?page=2",
		"/home/you/codes/ntikuma/main.go",
		"~/Documents/notes.md",
		"user_id_2",
		"fmt.Println(\"hello\")",
		"snake_case_name_2024",
		"go test -race ./internal/...",
		"Meeting moved to 3pm on Thursday.",
		"The password field is required.",
		"hello",
		"",
	}
	for _, text := range ordinary {
		if looksSecret(text) {
			t.Errorf("flagged ordinary text %q", text)
		}
	}
}

func TestAutoClipboardHoldsBackSecretsButManualClipboardAttaches(t *testing.T) {
	c := NewContextService(nil, fakeClipboard{"sk-or-v1-0123456789abcdef0123456789abcdef\n", true}, nil, nil)
	auto, err := c.AutoClipboard()
	if err != nil || auto.Attachment != nil || !strings.Contains(auto.Message, "looks like a password or key") {
		t.Fatalf("auto: %+v %v", auto, err)
	}
	manual, err := c.Clipboard()
	if err != nil || manual == nil || manual.Content != "sk-or-v1-0123456789abcdef0123456789abcdef" {
		t.Fatalf("manual: %+v %v", manual, err)
	}
	c = NewContextService(nil, fakeClipboard{"ordinary copied text", true}, nil, nil)
	if auto, err := c.AutoClipboard(); err != nil || auto.Attachment == nil || auto.Attachment.Content != "ordinary copied text" || auto.Message != "" {
		t.Fatalf("ordinary: %+v %v", auto, err)
	}
	c = NewContextService(nil, fakeClipboard{"", false}, nil, nil)
	if auto, err := c.AutoClipboard(); err != nil || auto.Attachment != nil || auto.Message != "" {
		t.Fatalf("empty: %+v %v", auto, err)
	}
}
