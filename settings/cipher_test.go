package settings_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nickwhiteley/plinth/code"

	"github.com/nickwhiteley/plinth/settings"
)

func TestCipherRoundTrip(t *testing.T) {
	c, err := settings.NewCipher(key(t))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	const secret = "sk_live_0123456789 with spaces, £ and a # that is not a comment"

	sealed, err := c.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if strings.Contains(string(sealed), "sk_live") {
		t.Fatal("the plaintext is visible in the stored value")
	}
	got, err := c.Decrypt(sealed)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != secret {
		t.Fatalf("round trip = %q", got)
	}
}

func TestEncryptingTwiceProducesDifferentBytes(t *testing.T) {
	// A fresh nonce per call, so the shadow log cannot leak that a secret was
	// set back to a value it held before.
	c, _ := settings.NewCipher(key(t))
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if string(a) == string(b) {
		t.Fatal("two encryptions of one value are byte-identical")
	}
}

func TestTamperingIsRejected(t *testing.T) {
	c, _ := settings.NewCipher(key(t))
	sealed, _ := c.Encrypt("stub")

	// GCM authenticates, so a flipped character is an error rather than
	// rubbish flowing on into a configuration. Flipped in the middle: the
	// trailing characters of a base64 string can carry padding bits that
	// decode to the same bytes, which is a way to write this test so that it
	// passes without proving anything.
	tampered := append([]byte(nil), sealed...)
	mid := len(tampered) / 2
	if tampered[mid] == 'A' {
		tampered[mid] = 'B'
	} else {
		tampered[mid] = 'A'
	}
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("a tampered value decrypted")
	}

	if _, err := c.Decrypt(sealed[:4]); err == nil {
		t.Fatal("a truncated value decrypted")
	}
	if _, err := c.Decrypt([]byte("not base64 at all !!")); err == nil {
		t.Fatal("rubbish decoded")
	}
}

func TestTheWrongKeyCannotRead(t *testing.T) {
	a, _ := settings.NewCipher(key(t))
	b, _ := settings.NewCipher(key(t))
	sealed, _ := a.Encrypt("secret")

	_, err := b.Decrypt(sealed)
	if !errors.Is(err, settings.ErrUndecryptable) {
		t.Fatalf("a value decrypted under a different key: %v", err)
	}
	// It names the key in use by fingerprint, so a boot log can be compared
	// with the shell without the key ever being printed.
	var ce *code.Error
	if !errors.As(err, &ce) || ce.Params["fingerprint"] != b.Fingerprint() {
		t.Errorf("the error does not carry the fingerprint: %v", err)
	}
}

func TestAKeyOfTheWrongLengthIsRefused(t *testing.T) {
	// Truncating would encrypt everything under something nobody intended and
	// decrypt nothing on the next boot with a corrected key.
	if _, err := settings.NewCipher(make([]byte, 16)); !errors.Is(err, settings.ErrKeyLength) {
		t.Fatalf("a 16-byte key: %v", err)
	}
	if _, err := settings.NewCipher(nil); !errors.Is(err, settings.ErrNoKey) {
		t.Fatalf("an absent key: %v", err)
	}
}

func TestFingerprintIsStableAndDistinguishesKeys(t *testing.T) {
	raw := key(t)
	a, _ := settings.NewCipher(raw)
	again, _ := settings.NewCipher(raw)
	other, _ := settings.NewCipher(key(t))

	if len(a.Fingerprint()) != 12 {
		t.Fatalf("fingerprint %q: want 12 hex digits", a.Fingerprint())
	}
	if a.Fingerprint() != again.Fingerprint() {
		t.Error("the same key gave two fingerprints")
	}
	if a.Fingerprint() == other.Fingerprint() {
		t.Error("two keys gave one fingerprint")
	}
}

func TestFingerprintMatchesTheShellRecipe(t *testing.T) {
	// What `head -c32 /dev/zero | sha256sum | cut -c1-12` prints: the recipe in
	// Fingerprint's comment has to agree with the log line.
	c, _ := settings.NewCipher(make([]byte, 32))
	if got, want := c.Fingerprint(), "66687aadf862"; got != want {
		t.Errorf("fingerprint = %s, want %s", got, want)
	}
}

func TestDecodeKey(t *testing.T) {
	raw := key(t)
	for _, encoded := range []string{
		b64std(raw), b64url(raw), b64rawstd(raw),
	} {
		got, err := settings.DecodeKey(encoded)
		if err != nil {
			t.Fatalf("DecodeKey(%q): %v", encoded, err)
		}
		if string(got) != string(raw) {
			t.Fatalf("DecodeKey round trip failed for %q", encoded)
		}
	}
	if _, err := settings.DecodeKey(""); err == nil {
		t.Error("an empty key decoded")
	}
	if _, err := settings.DecodeKey("not base64 !!!"); err == nil {
		t.Error("rubbish decoded")
	}
}
