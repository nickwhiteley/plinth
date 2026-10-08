package blob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMemoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()

	if _, err := m.Get(ctx, "missing"); !errors.Is(err, ErrNoSuchKey) {
		t.Errorf("Get on a missing key = %v, want ErrNoSuchKey", err)
	}

	if err := m.Put(ctx, "a/one", []byte("first")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := m.Get(ctx, "a/one")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Data) != "first" {
		t.Errorf("Get = %q, want %q", got.Data, "first")
	}

	// Put replaces unconditionally: there is no compare-and-swap to opt into.
	if err := m.Put(ctx, "a/one", []byte("second")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, _ = m.Get(ctx, "a/one")
	if string(got.Data) != "second" {
		t.Errorf("after overwrite Get = %q, want %q", got.Data, "second")
	}
}

// Callers must not be able to change stored bytes by holding what they were
// given, or by reusing the slice they handed over. A real object store copies
// over the network, so a map-backed one has to copy too or it would be a more
// forgiving contract than production.
func TestMemoryCopiesOnTheWayInAndOut(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()

	source := []byte("original")
	if err := m.Put(ctx, "key", source); err != nil {
		t.Fatalf("Put: %v", err)
	}
	source[0] = 'X'

	got, _ := m.Get(ctx, "key")
	if string(got.Data) != "original" {
		t.Errorf("mutating the caller's slice changed stored bytes: %q", got.Data)
	}

	got.Data[0] = 'Y'
	again, _ := m.Get(ctx, "key")
	if string(again.Data) != "original" {
		t.Errorf("mutating a returned slice changed stored bytes: %q", again.Data)
	}
}

func TestMemoryListIsPrefixedAndSorted(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	for _, key := range []string{"g/b", "g/a", "other/x", "g/c"} {
		if err := m.Put(ctx, key, nil); err != nil {
			t.Fatalf("Put(%s): %v", key, err)
		}
	}

	keys, err := m.List(ctx, "g/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"g/a", "g/b", "g/c"}; !slices.Equal(keys, want) {
		t.Errorf("List = %v, want %v", keys, want)
	}

	// An empty prefix lists everything, which is what the key-layout test uses.
	all, _ := m.List(ctx, "")
	if len(all) != 4 {
		t.Errorf("List(\"\") returned %d keys, want 4", len(all))
	}

	if empty, _ := m.List(ctx, "nothing/"); len(empty) != 0 {
		t.Errorf("List on an unused prefix = %v, want none", empty)
	}
}

func TestMemoryDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	if err := m.Put(ctx, "key", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for range 2 {
		if err := m.Delete(ctx, "key"); err != nil {
			t.Errorf("Delete: %v, want nil even when already absent", err)
		}
	}
	if _, err := m.Get(ctx, "key"); !errors.Is(err, ErrNoSuchKey) {
		t.Errorf("after Delete, Get = %v, want ErrNoSuchKey", err)
	}
}

// Dir must honour the same contract as Memory, or local development would be
// testing something production does not do. Running one suite against both is
// the only way that stays true.
func TestBucketContract(t *testing.T) {
	buckets := map[string]func(t *testing.T) Bucket{
		"memory": func(*testing.T) Bucket { return NewMemory() },
		"dir": func(t *testing.T) Bucket {
			d, err := NewDir(t.TempDir())
			if err != nil {
				t.Fatalf("NewDir: %v", err)
			}
			return d
		},
	}
	for name, make := range buckets {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			b := make(t)

			if _, err := b.Get(ctx, "nothing"); !errors.Is(err, ErrNoSuchKey) {
				t.Errorf("Get on a missing key = %v, want ErrNoSuchKey", err)
			}

			if err := b.Put(ctx, "a/one.json", []byte(`{"v":1}`)); err != nil {
				t.Fatalf("Put: %v", err)
			}
			first, err := b.Get(ctx, "a/one.json")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if string(first.Data) != `{"v":1}` || first.ETag == "" {
				t.Fatalf("Get = %+v", first)
			}

			// Compare-and-swap: the held ETag wins, a stale one is refused.
			if err := b.PutIfMatch(ctx, "a/one.json", []byte(`{"v":2}`), first.ETag); err != nil {
				t.Fatalf("PutIfMatch with a current ETag: %v", err)
			}
			if err := b.PutIfMatch(ctx, "a/one.json", []byte(`{"v":3}`), first.ETag); !errors.Is(err, ErrPreconditionFailed) {
				t.Errorf("PutIfMatch with a stale ETag = %v, want ErrPreconditionFailed", err)
			}
			after, _ := b.Get(ctx, "a/one.json")
			if string(after.Data) != `{"v":2}` {
				t.Errorf("the refused write landed: %s", after.Data)
			}

			// IfAbsent claims a free key once.
			if err := b.PutIfMatch(ctx, "a/two.json", []byte("x"), IfAbsent); err != nil {
				t.Fatalf("PutIfMatch(IfAbsent): %v", err)
			}
			if err := b.PutIfMatch(ctx, "a/two.json", []byte("y"), IfAbsent); !errors.Is(err, ErrPreconditionFailed) {
				t.Errorf("IfAbsent on a taken key = %v, want ErrPreconditionFailed", err)
			}

			if err := b.Put(ctx, "b/three.json", []byte("z")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			keys, err := b.List(ctx, "a/")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if want := []string{"a/one.json", "a/two.json"}; !slices.Equal(keys, want) {
				t.Errorf("List = %v, want %v", keys, want)
			}

			if err := b.Delete(ctx, "a/one.json"); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if err := b.Delete(ctx, "a/one.json"); err != nil {
				t.Errorf("second Delete = %v, want nil", err)
			}
			if _, err := b.Get(ctx, "a/one.json"); !errors.Is(err, ErrNoSuchKey) {
				t.Errorf("after Delete, Get = %v", err)
			}
		})
	}
}

// A key must not be able to write outside the bucket root.
func TestDirRejectsEscapingKeys(t *testing.T) {
	root := t.TempDir()
	d, err := NewDir(filepath.Join(root, "bucket"))
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	ctx := context.Background()

	if err := d.Put(ctx, "../escaped.json", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.json")); err == nil {
		t.Error("a key containing .. wrote outside the bucket root")
	}
}
