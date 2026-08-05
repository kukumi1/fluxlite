package cryptox

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// SSHKeyPair is a freshly generated login key for a managed node.
//
// The panel generates the pair itself and keeps the private half sealed in the
// database. Only the public half is ever handed to the node, so enrolling a
// machine never puts a usable credential on the wire.
type SSHKeyPair struct {
	// PrivateKeyPEM is the OpenSSH-format private key, for sealed storage.
	PrivateKeyPEM []byte
	// AuthorizedKey is the single-line public key for authorized_keys.
	AuthorizedKey string
}

// GenerateSSHKeyPair creates an ed25519 key pair for node enrollment.
func GenerateSSHKeyPair(comment string) (*SSHKeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("build public key: %w", err)
	}

	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		authorized += " " + comment
	}

	return &SSHKeyPair{
		PrivateKeyPEM: pem.EncodeToMemory(block),
		AuthorizedKey: authorized,
	}, nil
}
