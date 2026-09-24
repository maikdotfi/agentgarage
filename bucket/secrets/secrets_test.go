package secrets_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/secrets"
)

func setup(t *testing.T) (*bucket.Caller, *bucket.Fake, string) {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(nil)
	store := bucket.NewFake()
	c := bucket.New(store, key, nil, map[string]bucket.Limit{"secrets": {Every: time.Millisecond, Burst: 10}})
	return c.Caller("secrets"), store, filepath.Join(t.TempDir(), "master.key")
}

func TestSecretWrittenToRecipientIsReadByMaster(t *testing.T) {
	ctx := context.Background()
	c, _, path := setup(t)
	recipient, err := secrets.GenerateMaster(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(ctx, c, recipient, "GH_TOKEN", "ghp_hunter2"); err != nil {
		t.Fatal(err)
	}

	master, err := secrets.LoadMaster(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := master.Get(ctx, c, "GH_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ghp_hunter2" {
		t.Errorf("got %q, want the token", got)
	}
}

func TestBucketNeverHoldsPlaintext(t *testing.T) {
	ctx := context.Background()
	c, store, path := setup(t)
	recipient, _ := secrets.GenerateMaster(path)
	secrets.Put(ctx, c, recipient, "GH_TOKEN", "ghp_hunter2")

	obj, err := store.Get(ctx, "secrets/GH_TOKEN.age")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(obj.Body, []byte("hunter2")) {
		t.Error("the bucket holds the plaintext")
	}
}

func TestAnotherMasterCannotRead(t *testing.T) {
	ctx := context.Background()
	c, _, path := setup(t)
	recipient, _ := secrets.GenerateMaster(path)
	secrets.Put(ctx, c, recipient, "GH_TOKEN", "ghp_hunter2")

	otherPath := filepath.Join(t.TempDir(), "other.key")
	secrets.GenerateMaster(otherPath)
	other, _ := secrets.LoadMaster(otherPath)
	if _, err := other.Get(ctx, c, "GH_TOKEN"); err == nil {
		t.Error("want an error decrypting with the wrong master key")
	}
}

func TestMasterKeyFileIsPrivateAndNeverOverwritten(t *testing.T) {
	_, _, path := setup(t)
	if _, err := secrets.GenerateMaster(path); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := secrets.GenerateMaster(path); err == nil {
		t.Error("want GenerateMaster to refuse to overwrite the master key")
	}
}

func TestNamesThatAreNotPlainAreRefused(t *testing.T) {
	ctx := context.Background()
	c, _, path := setup(t)
	recipient, _ := secrets.GenerateMaster(path)
	for _, name := range []string{"", "../config/x", "a/b", "a.b"} {
		if err := secrets.Put(ctx, c, recipient, name, "v"); err == nil {
			t.Errorf("Put(%q) succeeded, want refused", name)
		}
	}
}

func TestAMasterKnowsItsRecipient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	recipient, err := secrets.GenerateMaster(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := secrets.LoadMaster(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Recipient() != recipient {
		t.Errorf("recipient = %q, want %q", m.Recipient(), recipient)
	}
}
