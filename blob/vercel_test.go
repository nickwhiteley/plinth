package blob

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// Live tests against a real Vercel Blob store.
//
// Skipped unless BLOB_READ_WRITE_TOKEN is set, so the default `make test` stays
// offline and fast. They exist because the Vercel Blob API has no published
// REST specification — the request shapes in vercel.go were read from the SDK
// source, and only a live call proves they are right. A Memory-only suite would
// pass forever while the driver sent the wrong headers.
//
// Everything is written under a run-scoped prefix and deleted afterwards.
func liveBucket(t *testing.T) (*Vercel, string) {
	t.Helper()
	token := os.Getenv("BLOB_READ_WRITE_TOKEN")
	if token == "" {
		t.Skip("BLOB_READ_WRITE_TOKEN is not set; skipping live Vercel Blob tests")
	}
	v, err := NewVercel(token, os.Getenv("BLOB_STORE_HOST"))
	if err != nil {
		t.Fatalf("NewVercel: %v", err)
	}

	// A store whose quota is exhausted refuses every write. Skipping says so
	// and self-heals when the window resets; failing would leave the suite red
	// for a month over something no change to this code can fix.
	probe := "livetest/quota-probe.json"
	if err := v.Put(context.Background(), probe, []byte("{}")); err != nil {
		if strings.Contains(err.Error(), "store_suspended") {
			t.Skip("the blob store is suspended (quota exhausted); skipping the live tests")
		}
		t.Fatalf("probing the store: %v", err)
	}
	_ = v.Delete(context.Background(), probe)

	// Leading underscores are rejected by the API, which cost two attempts to
	// discover, so the prefix starts with a letter.
	prefix := "livetest/" + time.Now().UTC().Format("20060102-150405.000") + "/"
	t.Cleanup(func() {
		ctx := context.Background()
		keys, err := v.List(ctx, prefix)
		if err != nil {
			t.Logf("cleanup: listing %s: %v", prefix, err)
			return
		}
		for _, key := range keys {
			if err := v.Delete(ctx, key); err != nil {
				t.Logf("cleanup: deleting %s: %v", key, err)
			}
		}
	})
	return v, prefix
}

func TestVercelRoundTrip(t *testing.T) {
	v, prefix := liveBucket(t)
	ctx := context.Background()
	key := prefix + "round-trip.json"

	if _, err := v.Get(ctx, key); !errors.Is(err, ErrNoSuchKey) {
		t.Errorf("Get on a missing key = %v, want ErrNoSuchKey", err)
	}

	if err := v.Put(ctx, key, []byte(`{"first":true}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := v.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Data) != `{"first":true}` {
		t.Errorf("Get = %q", got.Data)
	}
	if got.ETag == "" {
		t.Error("Get returned no ETag, so no conditional write is possible")
	}

	if err := v.Put(ctx, key, []byte(`{"second":true}`)); err != nil {
		t.Fatalf("overwriting: %v", err)
	}
	again, err := v.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if string(again.Data) != `{"second":true}` {
		t.Errorf("after overwrite Get = %q", again.Data)
	}
	if again.ETag == got.ETag {
		t.Error("the ETag did not change when the content did")
	}
}

// The property the whole storage design now rests on. If Vercel ever withdraws
// conditional writes, this is what says so.
func TestVercelConditionalWrite(t *testing.T) {
	v, prefix := liveBucket(t)
	ctx := context.Background()
	key := prefix + "cas.json"

	if err := v.Put(ctx, key, []byte(`{"rev":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	first, err := v.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Writing with the ETag we hold succeeds.
	if err := v.PutIfMatch(ctx, key, []byte(`{"rev":2}`), first.ETag); err != nil {
		t.Fatalf("PutIfMatch with a current ETag: %v", err)
	}

	// Writing again with the now-stale ETag must be refused, not silently win.
	err = v.PutIfMatch(ctx, key, []byte(`{"rev":3}`), first.ETag)
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("PutIfMatch with a stale ETag = %v, want ErrPreconditionFailed", err)
	}

	// And the refused write must not have landed.
	after, err := v.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(after.Data) != `{"rev":2}` {
		t.Errorf("stored content = %q, want the accepted write to have survived", after.Data)
	}
}

func TestVercelIfAbsent(t *testing.T) {
	v, prefix := liveBucket(t)
	ctx := context.Background()
	key := prefix + "create-once.json"

	if err := v.PutIfMatch(ctx, key, []byte(`{"created":true}`), IfAbsent); err != nil {
		t.Fatalf("PutIfMatch(IfAbsent) on a free key: %v", err)
	}
	err := v.PutIfMatch(ctx, key, []byte(`{"created":"again"}`), IfAbsent)
	if !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("PutIfMatch(IfAbsent) on a taken key = %v, want ErrPreconditionFailed", err)
	}
}

func TestVercelListAndDelete(t *testing.T) {
	v, prefix := liveBucket(t)
	ctx := context.Background()

	keys := []string{prefix + "a.json", prefix + "b.json", prefix + "nested/c.json"}
	for _, key := range keys {
		if err := v.Put(ctx, key, []byte(`{}`)); err != nil {
			t.Fatalf("Put(%s): %v", key, err)
		}
	}

	listed, err := v.List(ctx, prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := slices.Clone(keys)
	slices.Sort(want)
	if !slices.Equal(listed, want) {
		t.Errorf("List = %v, want %v", listed, want)
	}

	// Listing is prefix-scoped, or Delete would walk another prefix's objects.
	nested, err := v.List(ctx, prefix+"nested/")
	if err != nil {
		t.Fatalf("List(nested): %v", err)
	}
	if len(nested) != 1 || nested[0] != prefix+"nested/c.json" {
		t.Errorf("List(nested) = %v", nested)
	}

	if err := v.Delete(ctx, keys[0]); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := v.Get(ctx, keys[0]); !errors.Is(err, ErrNoSuchKey) {
		t.Errorf("after Delete, Get = %v, want ErrNoSuchKey", err)
	}
	// Deleting twice must not error: cleanup has to be retryable.
	if err := v.Delete(ctx, keys[0]); err != nil {
		t.Errorf("second Delete = %v, want nil", err)
	}
}

// A cold start with no configured host must still work: serverless gives us a
// fresh process per invocation, so this is the common case, not an edge one.
func TestVercelLearnsItsStoreHost(t *testing.T) {
	v, prefix := liveBucket(t)
	ctx := context.Background()
	key := prefix + "cold-start.json"

	if err := v.Put(ctx, key, []byte(`{"x":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	cold, err := NewVercel(v.token, "")
	if err != nil {
		t.Fatalf("NewVercel: %v", err)
	}
	got, err := cold.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get from a bucket with no configured host: %v", err)
	}
	if string(got.Data) != `{"x":1}` {
		t.Errorf("Get = %q", got.Data)
	}
	if cold.storeHost == "" {
		t.Error("the store host was not learned")
	}
}
