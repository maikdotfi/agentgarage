package hosting

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
)

// Snapshot writes a consistent copy of one database to path, such as with
// VACUUM INTO. Only the process that owns the database can take one.
type Snapshot func(ctx context.Context, path string) error

// Backup snapshots each database and uploads it as <owner>/db/<day>.db, then
// points <owner>/db/latest at it. It returns the keys it wrote.
func Backup(ctx context.Context, c *bucket.Caller, day string, dbs map[string]Snapshot) ([]string, error) {
	dir, err := os.MkdirTemp("", "garage-backup")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var written []string
	for _, owner := range slices.Sorted(maps.Keys(dbs)) {
		path := filepath.Join(dir, filepath.Base(owner)+".db")
		if err := dbs[owner](ctx, path); err != nil {
			return written, fmt.Errorf("backup %s: %w", owner, err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return written, err
		}
		key := owner + "/db/" + day + ".db"
		if _, err := c.Put(ctx, key, body); err != nil {
			return written, err
		}
		if _, err := c.Put(ctx, owner+"/db/latest", []byte(day)); err != nil {
			return written, err
		}
		written = append(written, key)
	}
	return written, nil
}

// Restore brings back the latest snapshot of each database that is missing
// from disk, as owner to local path. It never overwrites a file, and an owner
// with no snapshots is left to start fresh. It returns the paths it wrote.
func Restore(ctx context.Context, c *bucket.Caller, dbs map[string]string) ([]string, error) {
	var restored []string
	for _, owner := range slices.Sorted(maps.Keys(dbs)) {
		path := dbs[owner]
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		latest, err := c.Get(ctx, owner+"/db/latest")
		if errors.Is(err, bucket.ErrNotFound) {
			continue
		}
		if err != nil {
			return restored, err
		}
		day := string(latest.Body)
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			return restored, fmt.Errorf("restore %s: latest is %q, not a day", owner, day)
		}
		snap, err := c.Get(ctx, owner+"/db/"+day+".db")
		if err != nil {
			return restored, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return restored, err
		}
		if err := os.WriteFile(path+".restoring", snap.Body, 0o600); err != nil {
			return restored, err
		}
		if err := os.Rename(path+".restoring", path); err != nil {
			return restored, err
		}
		restored = append(restored, path)
	}
	return restored, nil
}
