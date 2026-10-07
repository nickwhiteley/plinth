package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/crypto/argon2"

	"github.com/nickwhiteley/plinth/code"
)

// MinPasswordLength is the only password rule: length, no composition
// requirements.
const MinPasswordLength = 8

// ErrInvalidHash is returned when a stored hash cannot be parsed, or names
// parameters outside what this package mints and accepts, which means the
// record is corrupt rather than the password wrong.
var ErrInvalidHash = code.New("identity.invalid_hash")

// ErrHashUnavailable is returned when no salt could be read from the system's
// entropy source. crypto/rand practically never fails, but every error this
// package returns is a code.
var ErrHashUnavailable = code.New("identity.hash_unavailable")

// randRead is crypto/rand.Read, replaceable so a test can make it fail.
var randRead = rand.Read

// Bounds on a stored hash's parameters. Minted hashes are well inside them;
// anything outside is corruption, refused before it reaches argon2.IDKey, which
// panics on t < 1, p < 1, m < 8p or a zero key length, and allocates whatever m
// says (plinth#2 review).
const (
	maxIterations = 100
	maxMemoryKiB  = 1 << 20 // 1 GiB
	minSaltLen    = 8
	maxSaltLen    = 64
	minKeyLen     = 16
	maxKeyLen     = 64
)

// errWeak is ErrWeakPassword with the minimum, which a message needs.
var errWeak = ErrWeakPassword.With("min", strconv.Itoa(MinPasswordLength))

// argon2Params are the Argon2id cost parameters. Encoded into every hash, so
// raising them later still verifies existing passwords.
type argon2Params struct {
	memoryKiB   uint32
	iterations  uint32
	parallelism uint8
	saltLen     uint32
	keyLen      uint32
}

func defaultParams() argon2Params {
	// 64 MiB and 3 passes is the common interactive-login baseline.
	p := uint8(min(max(runtime.NumCPU(), 1), 4))
	return argon2Params{
		memoryKiB:   64 * 1024,
		iterations:  3,
		parallelism: p,
		saltLen:     16,
		keyLen:      32,
	}
}

// reducedParams are the cost parameters a Service built with
// WithReducedHashCost uses. See that function for why they exist.
//
// 8 MiB and one pass: enough that the PHC encoding, the salt handling and the
// concurrency bound are all still exercised for real, and about two hundred
// times cheaper than the production factor.
func reducedParams() argon2Params {
	p := defaultParams()
	p.memoryKiB = 8 * 1024
	p.iterations = 1
	p.parallelism = 1
	return p
}

// MaxConcurrentHashes bounds how many Argon2id operations run at once.
//
// Each reserves memoryKiB — 64 MiB — for its lifetime, and an unauthenticated
// login causes one whether or not the address exists. Unbounded, N
// simultaneous requests reserve 64N MiB, so the cost parameters that make a
// stored password expensive to attack make the endpoint that verifies it cheap
// to exhaust.
//
// Callers block rather than being refused. Queueing makes sign-in slow under
// load and the server's WriteTimeout sheds whatever queues too long; returning
// an error here would instead turn load into failed sign-ins, which is the
// worse failure because it looks to the user exactly like a wrong password.
const MaxConcurrentHashes = 4

// hashGate is the semaphore, plus a high-water mark the tests read. Counting
// is not instrumentation for its own sake: without it the bound can only be
// asserted by timing, which is the kind of test that passes on a fast machine
// whatever the code does.
type hashGate struct {
	slots chan struct{}
	live  atomic.Int64
	peak  atomic.Int64
}

var hashes = &hashGate{slots: make(chan struct{}, MaxConcurrentHashes)}

func (g *hashGate) enter() {
	g.slots <- struct{}{}
	live := g.live.Add(1)
	for {
		peak := g.peak.Load()
		if live <= peak || g.peak.CompareAndSwap(peak, live) {
			break
		}
	}
}

func (g *hashGate) leave() {
	g.live.Add(-1)
	<-g.slots
}

// HashPassword returns an encoded Argon2id digest in the standard PHC format:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
//
// At the production cost factor. Code holding a *Service should call its hash
// method instead, which honours WithReducedHashCost.
func HashPassword(password string) (string, error) {
	return hashPasswordWith(defaultParams(), password)
}

func hashPasswordWith(p argon2Params, password string) (string, error) {
	if len([]rune(password)) < MinPasswordLength {
		return "", errWeak
	}
	// After the length check, so a password that is too short is refused
	// without first waiting for a slot.
	hashes.enter()
	defer hashes.leave()

	salt := make([]byte, p.saltLen)
	if _, err := randRead(salt); err != nil {
		return "", ErrHashUnavailable
	}

	key := argon2.IDKey([]byte(password), salt, p.iterations, p.memoryKiB, p.parallelism, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memoryKiB, p.iterations, p.parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches the encoded hash. The
// comparison is constant time.
func VerifyPassword(password, encoded string) (bool, error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(derive(password, salt, p), want) == 1, nil
}

// derive holds a gate slot around the derivation only (decodeHash is cheap and
// must not hold one), and releases it however IDKey returns.
func derive(password string, salt []byte, p argon2Params) []byte {
	hashes.enter()
	defer hashes.leave()
	return argon2.IDKey([]byte(password), salt, p.iterations, p.memoryKiB, p.parallelism, p.keyLen)
}

func decodeHash(encoded string) (argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return argon2Params{}, nil, nil, ErrInvalidHash
	}

	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return argon2Params{}, nil, nil, ErrInvalidHash
	}

	// Strictly three parameters, in order, each a bounded decimal: Sscanf would
	// accept trailing junk and wrap out-of-range values.
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return argon2Params{}, nil, nil, ErrInvalidHash
	}
	var vals [3]uint64
	for i, prefix := range []string{"m=", "t=", "p="} {
		v, ok := strings.CutPrefix(fields[i], prefix)
		if !ok {
			return argon2Params{}, nil, nil, ErrInvalidHash
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return argon2Params{}, nil, nil, ErrInvalidHash
		}
		vals[i] = n
	}
	m, t, par := vals[0], vals[1], vals[2]
	if t < 1 || t > maxIterations || par < 1 || par > 255 || m < 8*par || m > maxMemoryKiB {
		return argon2Params{}, nil, nil, ErrInvalidHash
	}
	p := argon2Params{memoryKiB: uint32(m), iterations: uint32(t), parallelism: uint8(par)}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < minSaltLen || len(salt) > maxSaltLen {
		return argon2Params{}, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < minKeyLen || len(key) > maxKeyLen {
		return argon2Params{}, nil, nil, ErrInvalidHash
	}

	p.saltLen = uint32(len(salt))
	p.keyLen = uint32(len(key))
	return p, salt, key, nil
}
