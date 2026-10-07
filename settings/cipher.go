package settings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"strconv"

	"github.com/nickwhiteley/plinth/code"
)

// KeyVersion is the version stamped on values this build writes.
//
// Nothing reads it yet. The column ships because it is free now and a migration
// later, and rotation is built the first time a key actually has to change.
// Losing a key without it is recoverable: every secret is re-entered from
// the provider's own console, which is an afternoon rather than a disaster.
const KeyVersion int16 = 1

// EncryptionKeyBytes is the AES-256 key length.
const EncryptionKeyBytes = 32

// Cipher encrypts and decrypts setting values.
//
// **Pure, and deliberately not the store's job.** Ciphertext goes into a
// backend and ciphertext comes out, so the conformance suite tests storage
// rather than cryptography and no backend can get the encryption subtly wrong
// in its own way.
//
// What this protects against, stated honestly: a database dump, a backup, a
// read replica, a support engineer with query access. It does **not** protect
// against anyone who can read the process's environment, because that is where
// the key is.
type Cipher struct {
	aead        cipher.AEAD
	fingerprint string
}

// Fingerprint names the key without revealing it: the first twelve hex digits
// of the SHA-256 of the raw key bytes.
//
// For telling whether two places hold the same key — the boot log against a
// local shell — which is otherwise unanswerable, because the key itself must
// never be printed. Forty-eight bits of a hash of 256 random bits says nothing
// about the key. Taken over the decoded bytes, not the base64, because
// DecodeKey accepts four encodings of one key.
//
//	printf '%s' "$ENCRYPTION_KEY" | base64 -d | sha256sum | cut -c1-12
func (c *Cipher) Fingerprint() string { return c.fingerprint }

// EnvEncryptionKey is the conventional name of the environment variable that
// carries the key, base64. Required everywhere with no default: a default key in
// the source is a permissive path a misconfigured deployment can fall into.
// Generate one with `openssl rand -base64 32`.
const EnvEncryptionKey = "ENCRYPTION_KEY"

var (
	// ErrNoKey is returned when no encryption key is configured.
	ErrNoKey = code.New("settings.no_key")
	// ErrKeyLength is a key that isn't 32 bytes. It carries "bytes".
	ErrKeyLength = code.New("settings.key_length")
	// ErrKeyEncoding is a key that isn't base64.
	ErrKeyEncoding = code.New("settings.key_encoding")
	// ErrUndecryptable is a stored value that won't decrypt or decode. Almost
	// always it means the encryption key has changed. It carries "fingerprint",
	// the current key's, when the cipher knows it.
	ErrUndecryptable = code.New("settings.undecryptable")
)

// NewCipher builds a cipher from a raw 32-byte key.
//
// A wrong length is an error rather than a truncation: a key silently cut to
// size would encrypt everything under something nobody intended and decrypt
// nothing on the next boot with a corrected one.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) == 0 {
		return nil, ErrNoKey
	}
	if len(key) != EncryptionKeyBytes {
		return nil, ErrKeyLength.With("bytes", strconv.Itoa(len(key)))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrKeyLength.With("bytes", strconv.Itoa(len(key)))
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(key)
	return &Cipher{aead: aead, fingerprint: hex.EncodeToString(sum[:6])}, nil
}

// DecodeKey reads a base64 key as it is carried in the environment.
//
// Standard and URL encodings are both accepted, because the difference between
// them is two characters and a deployment should not fail over which tool
// generated the key.
func DecodeKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, ErrNoKey
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if key, err := enc.DecodeString(encoded); err == nil {
			return key, nil
		}
	}
	return nil, ErrKeyEncoding
}

// Encrypt returns nonce+ciphertext, base64 encoded for the column.
//
// A fresh random nonce per call, so encrypting the same value twice produces
// different bytes and the shadow log does not leak that a secret was set back
// to what it was before.
func (c *Cipher) Encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	out := make([]byte, base64.StdEncoding.EncodedLen(len(sealed)))
	base64.StdEncoding.Encode(out, sealed)
	return out, nil
}

// Decrypt reverses Encrypt.
//
// GCM authenticates, so a tampered or truncated value is an error rather than
// rubbish that flows on into a configuration.
func (c *Cipher) Decrypt(stored []byte) (string, error) {
	raw := make([]byte, base64.StdEncoding.DecodedLen(len(stored)))
	undecryptable := ErrUndecryptable.With("fingerprint", c.fingerprint)
	n, err := base64.StdEncoding.Decode(raw, stored)
	if err != nil {
		return "", undecryptable
	}
	raw = raw[:n]
	if len(raw) < c.aead.NonceSize() {
		return "", undecryptable
	}
	nonce, body := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", undecryptable
	}
	return string(plaintext), nil
}
