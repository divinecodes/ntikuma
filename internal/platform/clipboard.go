package platform

import "slices"

// passwordManagerHint is the type KeePassXC and KWalletManager add to a copied
// password, so clipboard managers and syncing tools leave it alone. GTK4 lists
// it on Wayland, but its X11 backend drops types without a "/", so there the
// hint is never seen.
const passwordManagerHint = "x-kde-passwordManagerHint"

// TextClipboard reads clipboard text. *application.ClipboardManager satisfies it.
type TextClipboard interface {
	Text() (string, bool)
}

// Clipboard reads text through the application's clipboard and also reports
// when a password manager marked the copied data as secret.
type Clipboard struct {
	text   TextClipboard
	invoke func(func())
}

// NewClipboard uses the application's GTK thread for the type check, like
// NewSelectionReader.
func NewClipboard(text TextClipboard, invoke func(func())) Clipboard {
	return Clipboard{text: text, invoke: invoke}
}

func (c Clipboard) Text() (string, bool) { return c.text.Text() }

// MarkedSecret reports a password manager's hint on the current clipboard.
// Like reading the text, it only sees other apps' data while Ntikuma has focus
// on Wayland.
func (c Clipboard) MarkedSecret() bool {
	var types []string
	c.invoke(func() { types = clipboardTypes() })
	return markedSecret(types)
}

func markedSecret(types []string) bool {
	return slices.Contains(types, passwordManagerHint)
}
