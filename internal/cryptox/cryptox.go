// Package cryptox provides at-rest encryption for node credentials and
// password hashing for operator accounts.
//
// Node credentials are the crown jewels: they are SSH keys and passwords for
// every managed machine. They are sealed with AES-256-GCM under a master key
// that lives outside the database, so a stolen database file alone is inert.
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// MasterKeySize is the AES-256 key length.
	MasterKeySize = 32

	EnvMasterKey     = "FLUXLITE_MASTER_KEY"
	EnvMasterKeyFile = "FLUXLITE_MASTER_KEY_FILE"
)

var (
	ErrNoMasterKey      = errors.New("master key not configured")
	ErrMasterKeySize    = fmt.Errorf("master key must be %d bytes", MasterKeySize)
	ErrCiphertextShort  = errors.New("ciphertext too short")
	ErrPasswordHashForm = errors.New("malformed password hash")
)

// Sealer encrypts and decrypts secrets under a process-wide master key.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer builds a Sealer from a raw 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != MasterKeySize {
		return nil, ErrMasterKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// LoadMasterKey reads the master key from FLUXLITE_MASTER_KEY (hex) or from
// the file named by FLUXLITE_MASTER_KEY_FILE. It never falls back to a
// built-in default: an absent key must stop the process, not silently
// downgrade every stored credential to plaintext-equivalent security.
func LoadMasterKey() ([]byte, error) {
	if path := os.Getenv(EnvMasterKeyFile); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read master key file: %w", err)
		}
		return decodeMasterKey(strings.TrimSpace(string(raw)))
	}
	if v := os.Getenv(EnvMasterKey); v != "" {
		return decodeMasterKey(strings.TrimSpace(v))
	}
	return nil, ErrNoMasterKey
}

func decodeMasterKey(s string) ([]byte, error) {
	if key, err := hex.DecodeString(s); err == nil && len(key) == MasterKeySize {
		return key, nil
	}
	if key, err := base64.StdEncoding.DecodeString(s); err == nil && len(key) == MasterKeySize {
		return key, nil
	}
	return nil, ErrMasterKeySize
}

// GenerateMasterKey returns a new random key as a hex string, for operators
// to place into their environment.
func GenerateMasterKey() (string, error) {
	key := make([]byte, MasterKeySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate master key: %w", err)
	}
	return hex.EncodeToString(key), nil
}

// Seal encrypts plaintext, returning nonce||ciphertext.
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func (s *Sealer) Open(sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrCiphertextShort
	}
	plaintext, err := s.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// Argon2id parameters. Tuned for an interactive login on a 2-core VPS:
// roughly 64 MiB and a few hundred milliseconds per verification.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns an encoded Argon2id hash of the password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches the encoded hash. The
// comparison is constant time so a timing side channel cannot leak the hash.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrPasswordHashForm
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrPasswordHashForm
	}
	if version != argon2.Version {
		return false, fmt.Errorf("%w: unsupported version %d", ErrPasswordHashForm, version)
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, ErrPasswordHashForm
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrPasswordHashForm
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrPasswordHashForm
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// RandomToken returns a URL-safe random token with n bytes of entropy.
func RandomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
