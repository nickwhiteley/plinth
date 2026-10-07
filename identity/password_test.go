package identity

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nickwhiteley/plinth/code"
)

func TestHashAndVerify(t *testing.T) {
	const pw = "correct horse battery staple"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash is not in PHC format: %s", hash)
	}
	if strings.Contains(hash, pw) {
		t.Error("hash contains the plaintext password")
	}

	ok, err := VerifyPassword(pw, hash)
	if err != nil || !ok {
		t.Errorf("VerifyPassword(correct) = %v, %v; want true, nil", ok, err)
	}
	ok, err = VerifyPassword("correct horse battery stapl", hash)
	if err != nil || ok {
		t.Errorf("VerifyPassword(wrong) = %v, %v; want false, nil", ok, err)
	}
}

func TestHashIsSaltedPerCall(t *testing.T) {
	a, _ := HashPassword("a very long password")
	b, _ := HashPassword("a very long password")
	if a == b {
		t.Error("identical passwords produced identical hashes; salt is not random")
	}
}

func TestHashRejectsShortPassword(t *testing.T) {
	_, err := HashPassword(strings.Repeat("a", MinPasswordLength-1))
	var ce *code.Error
	if !errors.Is(err, ErrWeakPassword) || !errors.As(err, &ce) || ce.Params["min"] != "8" {
		t.Errorf("error = %v, want ErrWeakPassword carrying min 8", err)
	}
	if _, err := HashPassword(strings.Repeat("a", MinPasswordLength)); err != nil {
		t.Errorf("password of exactly the minimum length was rejected: %v", err)
	}
}

// The value, not just the rule. The frontend hardcodes the same number for
// its `minlength` attribute — there is no generator for constants — so a change
// here has to be a deliberate one that prompts changing it there too.
func TestMinPasswordLengthIsEight(t *testing.T) {
	if MinPasswordLength != 8 {
		t.Errorf("MinPasswordLength = %d; if that is intended, every product's sign-up form "+
			"and its \"at least N characters\" hint must change to match", MinPasswordLength)
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{
		"", "not-a-hash", "$argon2id$", "$bcrypt$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=99$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=19$bogus$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA",
	} {
		if _, err := VerifyPassword("whatever", bad); err == nil {
			t.Errorf("VerifyPassword(%q) returned no error", bad)
		}
	}
}

// Argon2id reserves 64 MiB per operation and an unauthenticated login causes
// one whether or not the address exists, so without a bound N simultaneous
// requests reserve 64N MiB — the cost parameters that make a stored password
// expensive to attack making the endpoint that verifies it cheap to exhaust.
func TestConcurrentHashingIsBounded(t *testing.T) {
	hashes.peak.Store(0)

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := HashPassword("a-long-enough-password"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if peak := hashes.peak.Load(); peak > MaxConcurrentHashes {
		t.Errorf("peak concurrent hashes = %d, want at most %d", peak, MaxConcurrentHashes)
	}
	// The half that gives the test teeth. Comparing the peak only against the
	// constant is self-referential — raise the constant and the assertion
	// relaxes with it, so it holds for any bound at all, including none. With
	// 32 callers and a working gate the slots are saturated, so the peak must
	// *reach* the bound as well as not exceed it. That fails if the gate is
	// removed (the peak stays 0), if it never binds, and if the bound is
	// raised past what the callers here can fill.
	if peak := hashes.peak.Load(); peak < MaxConcurrentHashes {
		t.Errorf("peak concurrent hashes = %d with 32 callers, want %d; "+
			"the bound never bound", peak, MaxConcurrentHashes)
	}
}

// The cost factor is a security parameter, and WithReducedHashCost exists a
// few lines away from it. Nothing else asserts what NewLocal hashes at, so
// without this a provider built the ordinary way could start producing
// test-grade digests and every other test in the package would still pass —
// they all assert decisions, not work factors.
func TestDefaultHashCostIsTheProductionFactor(t *testing.T) {
	l := NewLocal(stubStore{}, stubAccounts{})

	encoded, err := l.hash("a sufficiently long password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// Read back off the encoded hash rather than off defaultParams(), so this
	// checks what a provider built the ordinary way actually writes.
	got, _, _, err := decodeHash(encoded)
	if err != nil {
		t.Fatalf("decodeHash: %v", err)
	}
	want := defaultParams()
	if got.memoryKiB != want.memoryKiB || got.iterations != want.iterations {
		t.Errorf("stored hash at m=%d,t=%d; want m=%d,t=%d",
			got.memoryKiB, got.iterations, want.memoryKiB, want.iterations)
	}
	if got.memoryKiB < 64*1024 || got.iterations < 3 {
		t.Errorf("stored hash at m=%d,t=%d, below the interactive-login baseline of m=65536,t=3",
			got.memoryKiB, got.iterations)
	}
}

// stubStore and stubAccounts satisfy NewLocal for tests that never reach them.
type stubStore struct{ Store }
type stubAccounts struct{ Accounts }
