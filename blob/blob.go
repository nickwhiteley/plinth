// Package blob is a minimal object store: opaque keys to bytes, with compare-and-swap.
//
// A product writes against Bucket and never against a vendor. Three drivers are held to one
// contract by the conformance suite:
//
//   - **Vercel**, for a deployment.
//   - **Dir**, a directory, for local development, so `make dev` with production credentials in
//     the environment cannot write to the real store.
//   - **Memory**, a map, for tests.
//
// # Conditional writes
//
// PutIfMatch is the whole reason this interface is worth having. Object stores
// vary in whether they offer a conditional write. Vercel Blob does, verified against a live
// store. Optimistic concurrency
// on head depends on it, so a driver that cannot do it is not a usable backend
// for this application rather than merely a slower one.
package blob

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
)

var (
	// ErrNoSuchKey is returned by Get for a key that does not exist.
	ErrNoSuchKey = errors.New("blob: no such key")

	// ErrPreconditionFailed is returned by PutIfMatch when the stored object
	// is not the one the caller read. Callers surface it as a revision
	// conflict rather than retrying: the caller's document is stale, so
	// rewriting it would be the lost update this exists to prevent.
	ErrPreconditionFailed = errors.New("blob: precondition failed")

	// ErrStoreBlocked is returned when the store itself is unavailable —
	// suspended for exceeding a quota, most likely.
	//
	// Distinct from ErrNoSuchKey for a reason that cost real confusion: a
	// private store answers 403 both for an object that is not there and for a
	// store that has been blocked, so collapsing the two made an outage look
	// like an empty database. A user whose storage is down was told their
	// account did not exist, and a migration reported "no such key" for data
	// that was sitting there intact. An outage must present as an outage.
	ErrStoreBlocked = errors.New("blob: the store is blocked")
)

// IfAbsent is the ETag to pass to PutIfMatch to mean "only if nothing is
// stored under this key". Distinct from an empty ETag by accident, so a bug
// that loses an ETag cannot silently turn a conditional write into a blind one.
const IfAbsent = "\x00if-absent"

// Object is stored content plus the ETag identifying this version of it.
type Object struct {
	Data []byte
	// ETag changes whenever the content does. Pass it back to PutIfMatch to
	// write only if nothing else has written in the meantime.
	ETag string
}

// Bucket is a flat key-to-bytes store.
type Bucket interface {
	// Get returns ErrNoSuchKey if the object does not exist.
	//
	// The read must be strongly consistent. A stale read cannot cause a lost
	// update — PutIfMatch would reject the write — but it does cause spurious
	// conflicts, which the user sees as a save that failed for no reason.
	Get(ctx context.Context, key string) (Object, error)

	// Put writes unconditionally, replacing whatever is there.
	Put(ctx context.Context, key string, data []byte) error

	// PutIfMatch writes only if the stored object's ETag is etag, returning
	// ErrPreconditionFailed otherwise. Pass IfAbsent to require that nothing
	// is stored yet.
	PutIfMatch(ctx context.Context, key string, data []byte, etag string) error

	// Delete removes an object. Deleting an absent key is not an error, so
	// cleanup is safe to retry.
	Delete(ctx context.Context, key string) error

	// List returns the keys under a prefix, in ascending order.
	//
	// Object stores make listing slow and sometimes not immediately
	// consistent, so it must not be on a hot read path.
	List(ctx context.Context, prefix string) ([]string, error)
}

// Memory is an in-process Bucket for tests.
//
// Its mutex makes each operation atomic, including the compare-and-swap in
// PutIfMatch — which is what a real object store provides too. It does not
// make *sequences* of operations atomic, so a caller that reads and then
// writes without a condition races exactly as it would against Vercel.
type Memory struct {
	mu      sync.RWMutex
	objects map[string]Object
}

func NewMemory() *Memory {
	return &Memory{objects: make(map[string]Object)}
}

var _ Bucket = (*Memory)(nil)

// etagOf derives a version marker from content. Vercel returns an MD5 of the
// body, quoted; matching that shape keeps the two drivers behaving alike, and
// content-derived ETags mean an identical rewrite is not a conflict.
func etagOf(data []byte) string {
	sum := md5.Sum(data)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (m *Memory) Get(_ context.Context, key string) (Object, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	obj, ok := m.objects[key]
	if !ok {
		return Object{}, ErrNoSuchKey
	}
	return Object{Data: slices.Clone(obj.Data), ETag: obj.ETag}, nil
}

func (m *Memory) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store(key, data)
	return nil
}

func (m *Memory) PutIfMatch(_ context.Context, key string, data []byte, etag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.objects[key]
	switch {
	case etag == IfAbsent:
		if ok {
			return ErrPreconditionFailed
		}
	case !ok:
		// The caller holds an ETag for something that no longer exists.
		return ErrPreconditionFailed
	case existing.ETag != etag:
		return ErrPreconditionFailed
	}

	m.store(key, data)
	return nil
}

// store writes under the write lock.
func (m *Memory) store(key string, data []byte) {
	clone := slices.Clone(data)
	m.objects[key] = Object{Data: clone, ETag: etagOf(clone)}
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// Clone returns an independent copy holding the same objects.
//
// For test fixtures that need the same seeded bucket many times over. Seeding
// is not cheap — the embedded catalogue is a few thousand records — and doing
// it once and copying the result is both faster and a stronger guarantee that
// every test starts from the same bytes. The copy shares nothing: Object.Data
// is cloned, so a test that writes cannot reach into the original.
func (m *Memory) Clone() *Memory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := &Memory{objects: make(map[string]Object, len(m.objects))}
	for k, obj := range m.objects {
		out.objects[k] = Object{Data: slices.Clone(obj.Data), ETag: obj.ETag}
	}
	return out
}

func (m *Memory) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out, nil
}
