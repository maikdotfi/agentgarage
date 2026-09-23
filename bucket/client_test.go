package bucket_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

var fast = bucket.Limit{Every: time.Millisecond, Burst: 100}

// pair is a laptop and a host sharing one bucket, each trusting the other.
func pair(t *testing.T) (laptop, host *bucket.Client, store *bucket.Fake) {
	t.Helper()
	lpub, lpriv := newKey(t)
	hpub, hpriv := newKey(t)
	store = bucket.NewFake()
	limits := map[string]bucket.Limit{"test": fast}
	laptop = bucket.New(store, lpriv, []ed25519.PublicKey{hpub}, limits)
	host = bucket.New(store, hpriv, []ed25519.PublicKey{lpub}, limits)
	return laptop, host, store
}

func TestTrustedWriterIsRead(t *testing.T) {
	ctx := context.Background()
	laptop, host, _ := pair(t)

	if _, err := laptop.Caller("test").Put(ctx, "config/a", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	obj, err := host.Caller("test").Get(ctx, "config/a")
	if err != nil {
		t.Fatal(err)
	}
	if string(obj.Body) != "hello" {
		t.Errorf("body = %q, want hello", obj.Body)
	}
}

func TestUntrustedWriterIsDropped(t *testing.T) {
	ctx := context.Background()
	_, host, store := pair(t)
	_, strangerKey := newKey(t)
	stranger := bucket.New(store, strangerKey, nil, map[string]bucket.Limit{"test": fast})

	if _, err := stranger.Caller("test").Put(ctx, "config/a", []byte("evil")); err != nil {
		t.Fatal(err)
	}
	_, err := host.Caller("test").Get(ctx, "config/a")
	if !errors.Is(err, bucket.ErrBadSignature) {
		t.Errorf("err = %v, want ErrBadSignature", err)
	}
}

func TestUnsignedObjectIsDropped(t *testing.T) {
	ctx := context.Background()
	_, host, store := pair(t)

	if _, err := store.Put(ctx, "config/a", bucket.Object{Body: []byte("raw")}, bucket.Cond{}); err != nil {
		t.Fatal(err)
	}
	_, err := host.Caller("test").Get(ctx, "config/a")
	if !errors.Is(err, bucket.ErrBadSignature) {
		t.Errorf("err = %v, want ErrBadSignature", err)
	}
}

func TestSignedObjectMovedToAnotherKeyIsDropped(t *testing.T) {
	ctx := context.Background()
	laptop, host, store := pair(t)

	if _, err := laptop.Caller("test").Put(ctx, "mail/to-host/1", []byte("deploy")); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Get(ctx, "mail/to-host/1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "mail/to-host/2", raw, bucket.Cond{}); err != nil {
		t.Fatal(err)
	}
	_, err = host.Caller("test").Get(ctx, "mail/to-host/2")
	if !errors.Is(err, bucket.ErrBadSignature) {
		t.Errorf("err = %v, want ErrBadSignature", err)
	}
}

func TestWriterReadsItsOwnObjects(t *testing.T) {
	ctx := context.Background()
	laptop, _, _ := pair(t)
	c := laptop.Caller("test")

	if _, err := c.Put(ctx, "garage/db/today.db", []byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "garage/db/today.db"); err != nil {
		t.Errorf("reading own write: %v", err)
	}
}

func TestMissingKeyIsNotFound(t *testing.T) {
	_, host, _ := pair(t)
	_, err := host.Caller("test").Get(context.Background(), "nope")
	if !errors.Is(err, bucket.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCreateIsCreateOnly(t *testing.T) {
	ctx := context.Background()
	laptop, _, _ := pair(t)
	c := laptop.Caller("test")

	if _, err := c.Create(ctx, "mail/to-host/1", []byte("first")); err != nil {
		t.Fatal(err)
	}
	_, err := c.Create(ctx, "mail/to-host/1", []byte("second"))
	if !errors.Is(err, bucket.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	obj, err := c.Get(ctx, "mail/to-host/1")
	if err != nil {
		t.Fatal(err)
	}
	if string(obj.Body) != "first" {
		t.Errorf("body = %q, want first", obj.Body)
	}
}

func TestSwapIsCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	laptop, host, _ := pair(t)
	c := laptop.Caller("test")

	etag, err := c.Put(ctx, "releases/current", []byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Swap(ctx, "releases/current", []byte("bbb"), etag); err != nil {
		t.Fatalf("swap with current etag: %v", err)
	}
	_, err = c.Swap(ctx, "releases/current", []byte("ccc"), etag)
	if !errors.Is(err, bucket.ErrConflict) {
		t.Fatalf("swap with stale etag: err = %v, want ErrConflict", err)
	}
	obj, err := host.Caller("test").Get(ctx, "releases/current")
	if err != nil {
		t.Fatal(err)
	}
	if string(obj.Body) != "bbb" {
		t.Errorf("body = %q, want bbb", obj.Body)
	}
}

func TestOperationsAreCountedByBillingClass(t *testing.T) {
	ctx := context.Background()
	laptop, host, store := pair(t)
	lc, hc := laptop.Caller("test"), host.Caller("test")

	lc.Put(ctx, "a", []byte("1"))
	lc.Create(ctx, "a", []byte("2")) // refused, still billed
	hc.Get(ctx, "a")
	hc.Get(ctx, "missing")

	want := bucket.Counts{ClassA: 2, ClassB: 2}
	if got := store.Counts(); got != want {
		t.Errorf("store counts = %+v, want %+v", got, want)
	}
	if got := laptop.Counts(); got != (bucket.Counts{ClassA: 2}) {
		t.Errorf("laptop counts = %+v", got)
	}
	if got := host.Counts(); got != (bucket.Counts{ClassB: 2}) {
		t.Errorf("host counts = %+v", got)
	}
}

func TestCallerOutOfTokensWaits(t *testing.T) {
	ctx := context.Background()
	_, priv := newKey(t)
	store := bucket.NewFake()
	c := bucket.New(store, priv, nil, map[string]bucket.Limit{
		"chat-poll": {Every: 50 * time.Millisecond, Burst: 1},
	}).Caller("chat-poll")

	start := time.Now()
	c.Get(ctx, "a")
	c.Get(ctx, "a")
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("two gets took %v, want the second to wait for a token", elapsed)
	}
}

func TestCancelledWaitNeverReachesTheBucket(t *testing.T) {
	_, priv := newKey(t)
	store := bucket.NewFake()
	c := bucket.New(store, priv, nil, map[string]bucket.Limit{
		"backup": {Every: time.Hour, Burst: 1},
	}).Caller("backup")

	c.Get(context.Background(), "a")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Get(ctx, "a"); err == nil {
		t.Fatal("want an error when the wait is cancelled")
	}
	if got := store.Counts().ClassB; got != 1 {
		t.Errorf("store saw %d gets, want 1", got)
	}
}

func TestUnknownCallerPanics(t *testing.T) {
	_, priv := newKey(t)
	c := bucket.New(bucket.NewFake(), priv, nil, nil)
	defer func() {
		if recover() == nil {
			t.Error("want a panic for a caller with no limit")
		}
	}()
	c.Caller("nobody")
}
