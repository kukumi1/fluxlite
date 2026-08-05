package cryptox

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func newTestSealer(t *testing.T) *Sealer {
	t.Helper()
	key := make([]byte, MasterKeySize)
	for i := range key {
		key[i] = byte(i)
	}
	s, err := NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return s
}

func TestSealOpenRoundTrip(t *testing.T) {
	s := newTestSealer(t)
	secret := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nnot a real key\n")

	sealed, err := s.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("sealed value still contains the plaintext")
	}

	opened, err := s.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, secret) {
		t.Errorf("opened = %q, want %q", opened, secret)
	}
}

// GCM must randomise the nonce, otherwise identical credentials would be
// distinguishable in the database.
func TestSealProducesDistinctCiphertexts(t *testing.T) {
	s := newTestSealer(t)

	first, err := s.Seal([]byte("same"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	second, err := s.Seal([]byte("same"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Error("sealing the same plaintext twice produced identical ciphertext")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	s := newTestSealer(t)
	sealed, err := s.Seal([]byte("credential"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff

	if _, err := s.Open(sealed); err == nil {
		t.Error("Open accepted a tampered ciphertext")
	}
}

func TestOpenRejectsShortInput(t *testing.T) {
	s := newTestSealer(t)
	if _, err := s.Open([]byte{1, 2, 3}); err != ErrCiphertextShort {
		t.Errorf("error = %v, want ErrCiphertextShort", err)
	}
}

func TestNewSealerRejectsWrongKeySize(t *testing.T) {
	if _, err := NewSealer([]byte("too short")); err != ErrMasterKeySize {
		t.Errorf("error = %v, want ErrMasterKeySize", err)
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	const password = "correct horse battery staple"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	ok, err := VerifyPassword(password, encoded)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Error("correct password was rejected")
	}

	ok, err = VerifyPassword("wrong password entirely", encoded)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Error("wrong password was accepted")
	}
}

func TestPasswordHashIsSalted(t *testing.T) {
	first, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Error("identical passwords produced identical hashes, salt is not applied")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, encoded := range []string{"", "plaintext", "$argon2id$v=19$m=1$notbase64$x"} {
		if _, err := VerifyPassword("whatever", encoded); err == nil {
			t.Errorf("VerifyPassword(%q) accepted a malformed hash", encoded)
		}
	}
}

func TestGenerateMasterKeyIsUsable(t *testing.T) {
	encoded, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	key, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("generated key is not hex: %v", err)
	}
	if len(key) != MasterKeySize {
		t.Fatalf("key length = %d, want %d", len(key), MasterKeySize)
	}
	if _, err := NewSealer(key); err != nil {
		t.Errorf("generated key rejected by NewSealer: %v", err)
	}
}

func TestDecodeMasterKeyAcceptsHexAndBase64(t *testing.T) {
	raw := make([]byte, MasterKeySize)
	for i := range raw {
		raw[i] = byte(i * 3)
	}

	fromHex, err := decodeMasterKey(hex.EncodeToString(raw))
	if err != nil {
		t.Fatalf("hex key rejected: %v", err)
	}
	if !bytes.Equal(fromHex, raw) {
		t.Error("hex decoding produced the wrong key")
	}

	if _, err := decodeMasterKey("short"); err != ErrMasterKeySize {
		t.Errorf("error = %v, want ErrMasterKeySize", err)
	}
}
