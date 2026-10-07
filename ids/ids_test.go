package ids

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/nickwhiteley/plinth/code"
)

// The same vector as Furniture Magic's engine/ident, computed independently in Python, so the two
// encoders are held to one answer.
const (
	exampleExternal = "acc_01M483M100FHQSZF5J56890WK5"
	exampleUUID     = "01a1103a-0400-7c6f-9fbc-b22990907265"
)

func TestKnownVector(t *testing.T) {
	u, err := ParseUUID(exampleUUID)
	if err != nil {
		t.Fatal(err)
	}
	if got := Account.Format(u); got != exampleExternal {
		t.Fatalf("Format = %s, want %s", got, exampleExternal)
	}
	back, err := Account.Parse(exampleExternal)
	if err != nil || back != u {
		t.Fatalf("Parse = %v, %v", back, err)
	}
	if u.String() != exampleUUID || !u.IsV7() {
		t.Fatalf("String = %s, v7 %v", u, u.IsV7())
	}
}

func TestExtremes(t *testing.T) {
	var zero, max UUID
	for i := range max {
		max[i] = 0xff
	}
	if got := Identity.Format(zero); got != "idn_00000000000000000000000000" {
		t.Fatal(got)
	}
	if got := Identity.Format(max); got != "idn_7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Fatal(got)
	}
}

func TestParseRefuses(t *testing.T) {
	for _, c := range []struct{ in, code string }{
		{"idn_01M483M100FHQSZF5J56890WK5", "id_kind"},    // an identity id where an account is expected
		{"acc01M483M100FHQSZF5J56890WK5", "id_syntax"},   // no separator
		{"acc_01M483M100FHQSZF5J56890WK", "id_syntax"},   // 25 characters
		{"acc_01M483M100FHQSZF5J56890WK55", "id_syntax"}, // 27
		{"acc_81M483M100FHQSZF5J56890WK5", "id_syntax"},  // over 128 bits
		{"acc_01m483m100fhqszf5j56890wk5", "id_syntax"},  // lower case isn't canonical
		{"acc_01M483M100FHQSZF5J56890WKU", "id_syntax"},  // U, I, L and O aren't in the alphabet
		{"acc_01M483M100FHQSZF5J56890WKI", "id_syntax"},
		{"", "id_syntax"},
	} {
		if _, err := Account.Parse(c.in); !errors.Is(err, code.New(c.code)) {
			t.Errorf("Parse(%q): want %s, got %v", c.in, c.code, err)
		}
	}
	for _, s := range []string{"01a1103a04007c6f9fbcb22990907265", "01A1103A-0400-7C6F-9FBC-B22990907265", "01a1103a-0400-7c6f-9fbc-b2299090726g"} {
		if _, err := ParseUUID(s); !errors.Is(err, code.New("id_syntax")) {
			t.Errorf("ParseUUID(%q): %v", s, err)
		}
	}
}

// splitmix64, so the property test is deterministic.
type rng uint64

func (r *rng) next() uint64 {
	*r += 0x9e3779b97f4a7c15
	z := uint64(*r)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func TestRoundTripAndOrder(t *testing.T) {
	r := rng(7)
	us := make([]UUID, 2000)
	for i := range us {
		binary.BigEndian.PutUint64(us[i][:8], r.next())
		binary.BigEndian.PutUint64(us[i][8:], r.next())
		if back, err := Account.Parse(Account.Format(us[i])); err != nil || back != us[i] {
			t.Fatalf("round trip %x: %v", us[i], err)
		}
	}
	byBytes := append([]UUID(nil), us...)
	sort.Slice(byBytes, func(i, j int) bool { return bytes.Compare(byBytes[i][:], byBytes[j][:]) < 0 })
	byText := append([]UUID(nil), us...)
	sort.Slice(byText, func(i, j int) bool { return Account.Format(byText[i]) < Account.Format(byText[j]) })
	for i := range byBytes {
		if byBytes[i] != byText[i] {
			t.Fatal("the external form must sort as the uuid does")
		}
	}
}

func TestNewIsATimeOrderedV7(t *testing.T) {
	at := time.UnixMilli(1_760_000_000_123)
	u := NewAt(at, bytes.NewReader(bytes.Repeat([]byte{0xff}, 16)))
	if !u.IsV7() {
		t.Fatalf("%s is not a v7", u)
	}
	if ms := int64(binary.BigEndian.Uint64(append([]byte{0, 0}, u[:6]...))); ms != at.UnixMilli() {
		t.Fatalf("timestamp %d, want %d", ms, at.UnixMilli())
	}
	a, b := New(), New()
	if a == b || !a.IsV7() || !b.IsV7() {
		t.Fatalf("New: %s %s", a, b)
	}
	later := NewAt(at.Add(time.Millisecond), bytes.NewReader(make([]byte, 16)))
	if bytes.Compare(u[:], later[:]) >= 0 {
		t.Fatal("a later millisecond must sort after, whatever the random bits")
	}
}

func TestRegisterRefusesDuplicatesAndBadPrefixes(t *testing.T) {
	if p := Register("zzt"); p.Format(UUID{})[:4] != "zzt_" {
		t.Fatal("a product prefix formats")
	}
	for _, p := range []string{"acc", "idn", "tie", "zzt", "a", "toolong", "Acc", "a1c", ""} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Register(%q): want a panic", p)
				}
			}()
			Register(p)
		}()
	}
}

func TestTokensAreRandomAndOnlyTheirHashIsKept(t *testing.T) {
	a, b := Token(), Token()
	if a == b {
		t.Fatal("tokens repeat")
	}
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil || len(raw) != 32 {
		t.Fatalf("a token is 32 random bytes, base64url unpadded: %q %v", a, err)
	}
	if HashToken(a) != sha256.Sum256([]byte(a)) {
		t.Fatal("HashToken is SHA-256 of the token as sent")
	}
}
