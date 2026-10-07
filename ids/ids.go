// Package ids mints and formats identifiers (spec.md §7).
//
// Every key is a UUIDv7. The database stores bare uuids. At the API boundary an id is a registered
// prefix plus the 26-character Crockford base32 form of the same 128 bits, e.g.
// acc_01M483M2YGE1CTRQSGT39SE6KV. It sorts as the uuid does, and an identity id can't be passed
// where an account id is expected.
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"regexp"
	"sync"
	"time"

	"github.com/nickwhiteley/plinth/code"
)

// UUID is 16 bytes in RFC 9562 order.
type UUID [16]byte

// Prefix names the kind of thing an id names. It is part of the API contract.
type Prefix string

// plinth's own prefixes. A product registers its own with Register.
var (
	Account  = Register("acc")
	Identity = Register("idn")
	Tier     = Register("tie")
)

var (
	registry = map[Prefix]bool{}
	regMu    sync.Mutex
	prefixRe = regexp.MustCompile(`^[a-z]{2,4}$`)
)

// Register reserves a prefix. A prefix registered twice, or one that isn't two to four lower-case
// letters, is a programming error and panics, so two kinds can never share a prefix.
func Register(p string) Prefix {
	regMu.Lock()
	defer regMu.Unlock()
	if !prefixRe.MatchString(p) {
		panic("ids: malformed prefix " + `"` + p + `"`)
	}
	if registry[Prefix(p)] {
		panic("ids: prefix " + `"` + p + `"` + " registered twice")
	}
	registry[Prefix(p)] = true
	return Prefix(p)
}

var (
	errSyntax = code.New("id_syntax")
	errKind   = code.New("id_kind")
)

// alphabet is Crockford base32: no I, L, O or U.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var decodeTable = func() [256]int8 {
	var t [256]int8
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		t[alphabet[i]] = int8(i)
	}
	return t
}()

// Format writes prefix_ plus 26 Crockford characters. 26 × 5 = 130 bits, so the first character
// carries only the top three bits and is always 0–7.
func (p Prefix) Format(u UUID) string {
	var out [26]byte
	hi := binary.BigEndian.Uint64(u[:8])
	lo := binary.BigEndian.Uint64(u[8:])
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(p) + "_" + string(out[:])
}

// Parse reads the external form of an id with this prefix. Only the canonical form is accepted:
// upper case, no substitutions for I, L, O or U, and a first character no higher than 7, so no
// two strings name the same id.
func (p Prefix) Parse(s string) (UUID, error) {
	var u UUID
	n := len(p)
	if len(s) != n+1+26 || s[n] != '_' {
		for i := 0; i < len(s); i++ {
			if s[i] == '_' {
				if i > 0 && len(s) == i+1+26 && s[:i] != string(p) {
					return u, errKind.With("want", string(p)).With("got", s[:i])
				}
				break
			}
		}
		return u, errSyntax.With("input", s)
	}
	if s[:n] != string(p) {
		return u, errKind.With("want", string(p)).With("got", s[:n])
	}
	body := s[n+1:]
	if body[0] > '7' {
		return u, errSyntax.With("input", s)
	}
	var hi, lo uint64
	for i := 0; i < 26; i++ {
		v := decodeTable[body[i]]
		if v < 0 {
			return u, errSyntax.With("input", s)
		}
		hi = hi<<5 | lo>>59
		lo = lo<<5 | uint64(v)
	}
	binary.BigEndian.PutUint64(u[:8], hi)
	binary.BigEndian.PutUint64(u[8:], lo)
	return u, nil
}

// ParseUUID reads the canonical lower-case 8-4-4-4-12 form, as Postgres writes it.
func ParseUUID(s string) (UUID, error) {
	var u UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, errSyntax.With("input", s)
	}
	h := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	for i := 0; i < len(h); i++ {
		if c := h[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return u, errSyntax.With("input", s)
		}
	}
	if _, err := hex.Decode(u[:], []byte(h)); err != nil {
		return u, errSyntax.With("input", s)
	}
	return u, nil
}

// String is the canonical lower-case form.
func (u UUID) String() string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], u[10:16])
	return string(b[:])
}

// IsV7 is true for version 7 with the RFC 9562 variant.
func (u UUID) IsV7() bool { return u[6]>>4 == 7 && u[8]>>6 == 0b10 }

// IsZero reports the all-zero uuid, which no minted id is.
func (u UUID) IsZero() bool { return u == UUID{} }

// New mints a UUIDv7 from the clock and crypto/rand.
func New() UUID { return NewAt(time.Now(), rand.Reader) }

// NewAt mints a UUIDv7 for a given time from the given randomness: 48 bits of Unix milliseconds,
// then the version, 12 random bits, the variant and 62 random bits.
func NewAt(t time.Time, r io.Reader) UUID {
	var u UUID
	if _, err := io.ReadFull(r, u[6:]); err != nil {
		panic("ids: reading randomness: " + err.Error()) // crypto/rand doesn't fail on supported platforms
	}
	ms := uint64(t.UnixMilli())
	u[0], u[1], u[2], u[3], u[4], u[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	u[6] = 0x70 | u[6]&0x0f
	u[8] = 0x80 | u[8]&0x3f
	return u
}

// Token is a secret: 32 random bytes, base64url without padding. It is handed to its holder and
// never stored; store HashToken(token) instead.
func Token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: reading randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// HashToken is the stored form of a token: SHA-256 of the token as sent.
func HashToken(token string) [32]byte { return sha256.Sum256([]byte(token)) }
