package secrets

import (
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestConnectionKeysAreBoundToTheirOrigin(t *testing.T) {
	t.Setenv(EnvVar, "")
	keyring.MockInit()
	k := Keyring{}
	const id, origin = "c-0123456789ab", "https://api.openai.com"
	if got, err := k.ConnectionKey(id, origin); got != "" || err != nil {
		t.Fatalf("no key yet: %q %v", got, err)
	}
	if err := k.SetConnectionKey(id, origin, "  fake-test-key\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := k.ConnectionKey(id, origin); got != "fake-test-key" || err != nil {
		t.Fatalf("saved key: %q %v", got, err)
	}
	// Another address never gets the key, without any step to clear it.
	for _, other := range []string{"https://attacker.example", "http://api.openai.com", "https://api.openai.com:8443", ""} {
		if got, err := k.ConnectionKey(id, other); got != "" || err != nil {
			t.Fatalf("key returned for %q: %q %v", other, got, err)
		}
	}
	if got, _ := k.ConnectionKey("c-ba9876543210", origin); got != "" {
		t.Fatal("key leaked to another connection")
	}
	if k, _ := k.APIKey(); k != "" {
		t.Fatal("connection key leaked to OpenRouter")
	}
	if err := k.SetConnectionKey(id, origin, " "); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.ConnectionKey(id, origin); got != "" {
		t.Fatal("empty key didn't delete")
	}
	if err := k.SetConnectionKey(id, origin, "fake-test-key"); err != nil {
		t.Fatal(err)
	}
	if err := k.DeleteConnectionKey(id); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.ConnectionKey(id, origin); got != "" {
		t.Fatal("deleted key still readable")
	}
	if err := k.DeleteConnectionKey(id); err != nil {
		t.Fatalf("deleting a missing key: %v", err)
	}
	if err := k.SetConnectionKey(id, "", "fake-test-key"); err == nil {
		t.Fatal("saved a key without an origin")
	}
}

func TestConnectionKeyFailsClosedOnUnreadableEntries(t *testing.T) {
	keyring.MockInit()
	const id = "c-0123456789ab"
	// A bare key, as an older or foreign writer might leave, has no origin.
	if err := keyring.Set(service, connectionUser(id), "fake-test-key"); err != nil {
		t.Fatal(err)
	}
	if got, err := (Keyring{}).ConnectionKey(id, "https://api.openai.com"); got != "" || err == nil || !strings.Contains(err.Error(), "enter") {
		t.Fatalf("unbound key used: %q %v", got, err)
	}
	keyring.MockInitWithError(keyring.ErrSetDataTooBig)
	if _, err := (Keyring{}).ConnectionKey(id, "https://api.openai.com"); err == nil {
		t.Fatal("keyring error treated as no key")
	}
}
