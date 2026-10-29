package platform

import "testing"

type textClipboard string

func (t textClipboard) Text() (string, bool) { return string(t), t != "" }

func TestPasswordManagerHintMarksTheClipboardSecret(t *testing.T) {
	keepass := []string{"text/plain;charset=utf-8", "UTF8_STRING", "x-kde-passwordManagerHint"}
	if !markedSecret(keepass) {
		t.Fatal("missed the password manager hint")
	}
	for _, types := range [][]string{nil, {"text/plain;charset=utf-8", "text/html"}, {"x-kde-passwordmanagerhint-other"}} {
		if markedSecret(types) {
			t.Fatalf("marked %v secret", types)
		}
	}
	ran := false
	c := NewClipboard(textClipboard("copied"), func(f func()) { ran = true; f() })
	if text, ok := c.Text(); text != "copied" || !ok {
		t.Fatalf("text %q %v", text, ok)
	}
	// Without a display the type check finds nothing and holds nothing back.
	if c.MarkedSecret() || !ran {
		t.Fatal("type check did not run on the invoke thread or found a hint without a display")
	}
}
