package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"ntikuma/internal/config"
	"ntikuma/internal/screenshot"
	"ntikuma/internal/selection"
)

// captures context to attach message. Nothing is captured unless the frontend asks
// and every captures shows up as a removeable chips
type ContextService struct {
	cfg       *config.Manager
	clipboard Clipboard
	capture   func(context.Context) (screenshot.Image, error)
	selection selection.Reader
	mu        sync.Mutex
	cancel    context.CancelFunc
	closed    bool
}

// wires the context service
func NewContextService(cfg *config.Manager, cb Clipboard, capture func(context.Context) (screenshot.Image, error), primary selection.Reader) *ContextService {
	return &ContextService{cfg: cfg, clipboard: cb, capture: capture, selection: primary}
}

// clipboard returns the clipboard text as an attachment or nil when empty
// call it after window focus: Wayland only let's the focused app read the clipboard
func (c *ContextService) Clipboard() (*Attachment, error) {
	text, ok := c.clipboard.Text()
	if !ok {
		return nil, nil
	}
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}

	return &Attachment{Kind: "clipboard", Label: describe("Clipboard", text), Content: text}, nil
}

// ClipboardCapture is what AutoClipboard found. Message explains a clipboard
// that was held back; it is empty when Attachment is set or nothing was copied.
type ClipboardCapture struct {
	Attachment *Attachment `json:"attachment"`
	Message    string      `json:"message"`
}

// AutoClipboard is the capture made when the popup opens, before anyone asks.
// Text a password manager marked as secret, or that looks like a password,
// token or private key, is held back, since it would otherwise go to OpenRouter
// on the next question. Clipboard still attaches it when the user adds the
// clipboard themselves.
func (c *ContextService) AutoClipboard() (ClipboardCapture, error) {
	a, err := c.Clipboard()
	if err != nil || a == nil {
		return ClipboardCapture{}, err
	}
	if marker, ok := c.clipboard.(secretMarker); ok && marker.MarkedSecret() {
		return ClipboardCapture{Message: "A password manager marked the clipboard as secret, so it wasn't attached. Choose Clipboard to attach it anyway."}, nil
	}
	if looksSecret(a.Content) {
		return ClipboardCapture{Message: "The clipboard looks like a password or key, so it wasn't attached. Choose Clipboard to attach it anyway."}, nil
	}
	return ClipboardCapture{Attachment: a}, nil
}

var (
	secretAssignment = regexp.MustCompile(`(?im)^\s*(?:export\s+)?[\w.-]*(?:secret|token|password|passwd|api[_-]?key|private[_-]?key|access[_-]?key)[\w.-]*\s*[:=]\s*["']?[^\s"']{6,}`)
	secretPrefixes   = []string{
		"sk-", "sk_live_", "sk_test_", "rk_live_", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-",
		"xoxb-", "xoxp-", "xoxa-", "xapp-", "AKIA", "ASIA", "AIza", "ya29.", "hf_", "npm_", "pypi-", "shpat_", "SG.",
	}
)

// looksSecret is a heuristic, tuned to miss little: holding back ordinary
// text costs one click, while sending a secret can't be undone.
func looksSecret(text string) bool {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "-----BEGIN ") && strings.Contains(text, "PRIVATE KEY") {
		return true
	}
	if secretAssignment.MatchString(text) {
		return true
	}
	// Known token formats, also inside a command such as a curl header.
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("=:\"'`,;()[]{}<>", r)
	}) {
		if len(field) >= 20 {
			for _, prefix := range secretPrefixes {
				if strings.HasPrefix(field, prefix) {
					return true
				}
			}
		}
		if len(field) >= 30 && strings.HasPrefix(field, "eyJ") && strings.Count(field, ".") == 2 {
			return true
		}
	}
	// A single copied token: a password or a random key.
	if len(text) < 8 || len(text) > 256 || strings.IndexFunc(text, unicode.IsSpace) >= 0 || strings.Contains(text, "://") {
		return false
	}
	for _, prefix := range []string{"/", "~/", "./", "../"} {
		if strings.HasPrefix(text, prefix) {
			return false
		}
	}
	var upper, lower, digit, symbol, other bool
	for _, r := range text {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		case r == '-' || r == '_':
			symbol = true
		default:
			symbol, other = true, true
		}
	}
	if upper && lower && digit && symbol && len(text) <= 64 {
		return true
	}
	return !other && digit && (upper || lower) && len(text) >= 16 && ((upper && lower) || len(text) >= 32)
}

type SelectionCapture struct {
	Attachment *Attachment `json:"attachment"`
	Message    string      `json:"message"`
}

func (c *ContextService) SelectionStatus() selection.Support {
	if c.selection == nil {
		return selection.Support{Message: "Selected text is unavailable in this build. Copy the text and attach Clipboard instead."}
	}
	return c.selection.Support()
}

// Selection reads PRIMARY only. An empty selection never falls back to the
// normal copy/paste clipboard. X11 callers can read before raising the popup;
// Wayland callers must wait for native focus and its selection offer.
func (c *ContextService) Selection(ctx context.Context) (SelectionCapture, error) {
	if support := c.SelectionStatus(); !support.Available {
		return SelectionCapture{Message: support.Message}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	text, err := c.selection.Read(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return SelectionCapture{}, errors.New("selected text took too long to read; try again")
		}
		return SelectionCapture{}, err
	}
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return SelectionCapture{Message: "No selected text is available. The source application may not share highlighted text."}, nil
	}
	return SelectionCapture{Attachment: &Attachment{Kind: "selection", Label: describe("Selection", text), Content: text}}, nil
}

// Screenshot opens the desktop's interactive capture dialog. Cancellation
// returns no attachment and leaves the composer's text alone.
func (c *ContextService) Screenshot(ctx context.Context) (*Attachment, error) {
	c.mu.Lock()
	if c.closed || c.cancel != nil || c.capture == nil {
		c.mu.Unlock()
		return nil, errors.New("screenshot capture is unavailable or already open")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	c.cancel = cancel
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
	}()
	img, err := c.capture(ctx)
	if errors.Is(err, screenshot.ErrCancelled) || errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return nil, nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, errors.New("screenshot capture timed out; try again")
	}
	if err != nil {
		return nil, err
	}
	return &Attachment{Kind: "screenshot", Label: fmt.Sprintf("Screenshot · %d × %d", img.Width, img.Height), Content: img.DataURL}, nil
}

func (c *ContextService) CancelScreenshot() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *ContextService) ServiceShutdown() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}

// describe builds a chip lable such as Clipboard
func describe(source, text string) string {
	lines := strings.Count(text, "\n") + 1
	if lines == 1 {
		n := len([]rune(text))
		if n == 1 {
			return source + " · 1 character"
		}
		return fmt.Sprintf("%s · %d characters", source, n)
	}
	return fmt.Sprintf("%s · %d lines", source, lines)
}
