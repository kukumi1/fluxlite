package cryptox

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
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

// KeyIdentity is how a key appears in authorized_keys: its base64 blob and the
// comment after it.
type KeyIdentity struct {
	Blob    string
	Comment string
}

const opensshMagic = "openssh-key-v1\x00"

// ErrNoKeyComment means the key carries no comment the panel can rely on:
// it is encrypted, not in OpenSSH format, or not an ed25519 key the panel
// generated.
var ErrNoKeyComment = errors.New("private key has no readable comment")

// IdentityOf reads the authorized_keys identity of an OpenSSH private key.
//
// x/crypto/ssh parses these keys but drops the comment, and the comment is the
// only thing tying a line in authorized_keys to the node it was issued for.
// Only unencrypted ed25519 keys are read, which is every key the panel
// generates; anything else reports ErrNoKeyComment.
func IdentityOf(privateKeyPEM []byte) (*KeyIdentity, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil || block.Type != "OPENSSH PRIVATE KEY" || !strings.HasPrefix(string(block.Bytes), opensshMagic) {
		return nil, ErrNoKeyComment
	}

	var envelope struct {
		CipherName   string
		KdfName      string
		KdfOpts      string
		NumKeys      uint32
		PubKey       []byte
		PrivKeyBlock []byte
	}
	if err := ssh.Unmarshal(block.Bytes[len(opensshMagic):], &envelope); err != nil {
		return nil, fmt.Errorf("parse private key envelope: %w", err)
	}
	if envelope.CipherName != "none" || envelope.NumKeys != 1 {
		return nil, ErrNoKeyComment
	}

	var head struct {
		Check1  uint32
		Check2  uint32
		Keytype string
		Rest    []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(envelope.PrivKeyBlock, &head); err != nil {
		return nil, fmt.Errorf("parse private key block: %w", err)
	}
	if head.Check1 != head.Check2 || head.Keytype != ssh.KeyAlgoED25519 {
		return nil, ErrNoKeyComment
	}

	var body struct {
		Pub     []byte
		Priv    []byte
		Comment string
		Pad     []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(head.Rest, &body); err != nil {
		return nil, fmt.Errorf("parse ed25519 key: %w", err)
	}

	pub, err := ssh.ParsePublicKey(envelope.PubKey)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	fields := strings.Fields(string(ssh.MarshalAuthorizedKey(pub)))
	if len(fields) < 2 {
		return nil, fmt.Errorf("unexpected authorized key format")
	}
	return &KeyIdentity{Blob: fields[1], Comment: body.Comment}, nil
}
