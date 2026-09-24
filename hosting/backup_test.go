package hosting_test

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	tursodrv "turso.tech/database/tursogo"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/hosting"
)

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	conn, err := tursodrv.NewConnector(path)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(conn)
	t.Cleanup(func() { db.Close() })
	return db
}

// vacuum is how a database owner hands Backup a snapshot.
func vacuum(db *sql.DB) hosting.Snapshot {
	return func(ctx context.Context, path string) error {
		_, err := db.ExecContext(ctx, "VACUUM INTO '"+path+"'")
		return err
	}
}

func hostBucket(t *testing.T, store bucket.Store, key ed25519.PrivateKey) *bucket.Caller {
	t.Helper()
	return bucket.New(store, key, nil, map[string]bucket.Limit{"backup": {Every: time.Millisecond, Burst: 100}}).Caller("backup")
}

func TestARestoredHostHasYesterdaysData(t *testing.T) {
	ctx := context.Background()
	_, key, _ := ed25519.GenerateKey(nil)
	store := bucket.NewFake()
	db := openDB(t, filepath.Join(t.TempDir(), "chatroom.db"))
	db.Exec(`CREATE TABLE messages (text TEXT)`)
	db.Exec(`INSERT INTO messages VALUES ('remember me')`)

	written, err := hosting.Backup(ctx, hostBucket(t, store, key), "2026-09-24",
		map[string]hosting.Snapshot{"garage/chatroom": vacuum(db)})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(written, []string{"garage/chatroom/db/2026-09-24.db"}) {
		t.Errorf("wrote %q", written)
	}

	// A new host with the same key and an empty disk.
	path := filepath.Join(t.TempDir(), "home", "chatroom.db")
	restored, err := hosting.Restore(ctx, hostBucket(t, store, key), map[string]string{"garage/chatroom": path})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(restored, []string{path}) {
		t.Errorf("restored %q", restored)
	}
	var text string
	if err := openDB(t, path).QueryRow(`SELECT text FROM messages`).Scan(&text); err != nil || text != "remember me" {
		t.Errorf("restored database has %q, %v", text, err)
	}
}

func TestRestoreTakesTheLatestSnapshot(t *testing.T) {
	ctx := context.Background()
	_, key, _ := ed25519.GenerateKey(nil)
	store := bucket.NewFake()
	db := openDB(t, filepath.Join(t.TempDir(), "dev.db"))
	db.Exec(`CREATE TABLE notes (text TEXT)`)
	for _, day := range []string{"2026-09-23", "2026-09-24"} {
		db.Exec(`DELETE FROM notes`)
		db.Exec(`INSERT INTO notes VALUES (?)`, day)
		if _, err := hosting.Backup(ctx, hostBucket(t, store, key), day, map[string]hosting.Snapshot{"agents/dev": vacuum(db)}); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(t.TempDir(), "dev.db")
	before := store.Counts()
	hosting.Restore(ctx, hostBucket(t, store, key), map[string]string{"agents/dev": path})

	var text string
	openDB(t, path).QueryRow(`SELECT text FROM notes`).Scan(&text)
	if text != "2026-09-24" {
		t.Errorf("restored %q, want the latest day", text)
	}
	if c := store.Counts(); c.ClassA != before.ClassA || c.ClassB-before.ClassB != 2 {
		t.Errorf("restore took %d class B and %d class A ops, want 2 GETs", c.ClassB-before.ClassB, c.ClassA-before.ClassA)
	}
}

func TestRestoreNeverOverwritesADatabase(t *testing.T) {
	ctx := context.Background()
	_, key, _ := ed25519.GenerateKey(nil)
	store := bucket.NewFake()
	db := openDB(t, filepath.Join(t.TempDir(), "a.db"))
	db.Exec(`CREATE TABLE t (x TEXT)`)
	hosting.Backup(ctx, hostBucket(t, store, key), "2026-09-24", map[string]hosting.Snapshot{"agents/dev": vacuum(db)})

	path := filepath.Join(t.TempDir(), "dev.db")
	os.WriteFile(path, []byte("live data"), 0o600)
	restored, err := hosting.Restore(ctx, hostBucket(t, store, key), map[string]string{"agents/dev": path})
	if err != nil || len(restored) != 0 {
		t.Errorf("restored %q, %v; want nothing", restored, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "live data" {
		t.Errorf("the live database was overwritten")
	}
}

func TestRestoreWithNoSnapshotsIsAFreshStart(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	path := filepath.Join(t.TempDir(), "dev.db")
	restored, err := hosting.Restore(context.Background(), hostBucket(t, bucket.NewFake(), key), map[string]string{"agents/dev": path})
	if err != nil || len(restored) != 0 {
		t.Errorf("restored %q, %v; want nothing and no error", restored, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("restore made a database out of nothing")
	}
}

func TestSnapshotsFromAnotherKeyAreNotRestored(t *testing.T) {
	ctx := context.Background()
	_, key, _ := ed25519.GenerateKey(nil)
	_, other, _ := ed25519.GenerateKey(nil)
	store := bucket.NewFake()
	db := openDB(t, filepath.Join(t.TempDir(), "a.db"))
	db.Exec(`CREATE TABLE t (x TEXT)`)
	hosting.Backup(ctx, hostBucket(t, store, other), "2026-09-24", map[string]hosting.Snapshot{"agents/dev": vacuum(db)})

	path := filepath.Join(t.TempDir(), "dev.db")
	if _, err := hosting.Restore(ctx, hostBucket(t, store, key), map[string]string{"agents/dev": path}); err == nil {
		t.Error("restore accepted a snapshot signed by an untrusted key")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("an untrusted snapshot was written to disk")
	}
}
