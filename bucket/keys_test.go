package bucket_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/maikdotfi/agentgarage/bucket"
)

func TestGeneratedKeyIsTrustedByItsPublicKey(t *testing.T) {
	dir := t.TempDir()
	laptopPath := filepath.Join(dir, "laptop.key")
	pub, err := bucket.GenerateKey(laptopPath)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(laptopPath); info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := bucket.GenerateKey(laptopPath); err == nil {
		t.Error("want GenerateKey to refuse to overwrite a key")
	}

	trustedPath := filepath.Join(dir, "trusted.keys")
	os.WriteFile(trustedPath, []byte("# the laptop\n"+pub+"\n\n"), 0o644)

	laptopKey, err := bucket.LoadKey(laptopPath)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := bucket.LoadTrusted(trustedPath)
	if err != nil {
		t.Fatal(err)
	}
	_, hostKey := newKey(t)
	store := bucket.NewFake()
	limits := map[string]bucket.Limit{"test": fast}
	laptop := bucket.New(store, laptopKey, nil, limits)
	host := bucket.New(store, hostKey, trusted, limits)

	ctx := context.Background()
	laptop.Caller("test").Put(ctx, "k", []byte("v"))
	if _, err := host.Caller("test").Get(ctx, "k"); err != nil {
		t.Errorf("host reading laptop's write: %v", err)
	}
}

func TestLoadTrustedRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted.keys")
	os.WriteFile(path, []byte("not-a-key\n"), 0o644)
	if _, err := bucket.LoadTrusted(path); err == nil {
		t.Error("want an error for a line that is not a key")
	}
}
