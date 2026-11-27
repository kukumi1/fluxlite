package service

import (
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
)

// A key the panel could never log in with is refused when it is entered,
// with what is wrong with it, not at the first connection.
func TestCheckCredential(t *testing.T) {
	pair, err := cryptox.GenerateSSHKeyPair("test")
	if err != nil {
		t.Fatal(err)
	}
	crlf := strings.ReplaceAll(strings.TrimSpace(string(pair.PrivateKeyPEM)), "\n", "\r\n")
	for name, key := range map[string]string{"as generated": string(pair.PrivateKeyPEM), "pasted from Windows": crlf} {
		if err := checkCredential(model.AuthKey, key); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := checkCredential(model.AuthKey, pair.AuthorizedKey); err == nil || !strings.Contains(err.Error(), "公钥") {
		t.Fatalf("a public key is named as one: %v", err)
	}
	if err := checkCredential(model.AuthKey, "not a key"); err == nil {
		t.Fatal("text that is no key is refused")
	}
	if err := checkCredential(model.AuthPassword, "ssh-ed25519 looks like a key"); err != nil {
		t.Fatal("a password is taken as it is")
	}
}
