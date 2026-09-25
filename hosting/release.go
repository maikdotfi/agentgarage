package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/metaharness/agent"
	"github.com/maikdotfi/agentgarage/workspace"
)

// Releases is where a host keeps its garage binaries, owned by the garage
// user: <sha>/garage for each release, and current, the symlink to the one
// serve runs. serve-current, root's, points at current and never moves.
const Releases = "/opt/garage/releases"

// currentKey is the bucket's pointer to the release the host should run.
const currentKey = "releases/current"

func binaryKey(sha string) string { return "releases/" + sha + "/garage" }

var releaseName = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Publish uploads binary as release sha, signed by this side, and points
// releases/current at it. A host polling the bucket installs it from there.
func Publish(ctx context.Context, c *bucket.Caller, sha string, binary []byte) error {
	if !releaseName.MatchString(sha) {
		return fmt.Errorf("release: %q is not a sha", sha)
	}
	if _, err := c.Put(ctx, binaryKey(sha), binary); err != nil {
		return err
	}
	return point(ctx, c, sha)
}

// Point moves releases/current back (or on) to a release already in the
// bucket, which is how a rollback is done.
func Point(ctx context.Context, c *bucket.Caller, sha string) error {
	if !releaseName.MatchString(sha) {
		return fmt.Errorf("release: %q is not a sha", sha)
	}
	if _, err := c.Get(ctx, binaryKey(sha)); err != nil {
		return fmt.Errorf("release %s: %w", sha, err)
	}
	return point(ctx, c, sha)
}

// point compares and swaps releases/current, so two releases at once can't
// both think they won.
func point(ctx context.Context, c *bucket.Caller, sha string) error {
	cur, err := c.Get(ctx, currentKey)
	switch {
	case errors.Is(err, bucket.ErrNotFound):
		_, err = c.Create(ctx, currentKey, []byte(sha))
	case err == nil:
		_, err = c.Swap(ctx, currentKey, []byte(sha), cur.ETag)
	}
	if errors.Is(err, bucket.ErrExists) || errors.Is(err, bucket.ErrConflict) {
		return fmt.Errorf("release: %s moved while releasing %s, try again: %w", currentKey, sha, err)
	}
	return err
}

// Running is the release the host's current symlink points at, or "".
func Running(dir string) string {
	target, err := os.Readlink(filepath.Join(dir, "current"))
	if err != nil {
		return ""
	}
	return filepath.Dir(target)
}

// Update installs the release releases/current names, if the pointer moved
// since the last look, and returns its sha; "" means nothing changed. The
// first look only remembers, so a binary garage setup installed by hand stays
// until the next release. A binary is installed only if it is signed by a
// trusted key and runs here, and serve must then restart to run it. Each
// release is tried once.
func Update(ctx context.Context, c *bucket.Caller, dir string) (string, error) {
	seenPath := filepath.Join(dir, "seen")
	seen, err := os.ReadFile(seenPath)
	first := errors.Is(err, os.ErrNotExist)
	if err != nil && !first {
		return "", err
	}
	want := ""
	if cur, err := c.Get(ctx, currentKey); err == nil {
		want = strings.TrimSpace(string(cur.Body))
	} else if !errors.Is(err, bucket.ErrNotFound) {
		return "", err
	}
	if !first && want == string(seen) {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// A release is tried once: one that fails waits for the next, rather than
	// being downloaded again on every look.
	if err := os.WriteFile(seenPath, []byte(want), 0o644); err != nil {
		return "", err
	}
	if first || want == "" || want == Running(dir) {
		return "", nil
	}
	if err := install(ctx, c, dir, want); err != nil {
		return "", err
	}
	return want, nil
}

// install puts release sha in dir, unless it is there already, and points
// current at it.
func install(ctx context.Context, c *bucket.Caller, dir, sha string) error {
	if !releaseName.MatchString(sha) {
		return fmt.Errorf("release: %s names %q, not a sha", currentKey, sha)
	}
	path := filepath.Join(dir, sha, "garage")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		obj, err := c.Get(ctx, binaryKey(sha))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path+".new", obj.Body, 0o755); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, path+".new", "help").CombinedOutput(); err != nil {
			os.Remove(path + ".new")
			return fmt.Errorf("release %s does not run here: %w\n%s", sha, err, out)
		}
		if err := os.Rename(path+".new", path); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err := link(sha+"/garage", filepath.Join(dir, "current"))
	return err
}

// link points the symlink at path to target, atomically, and says whether it
// had to.
func link(target, path string) (bool, error) {
	if got, err := os.Readlink(path); err == nil && got == target {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	os.Remove(path + ".new")
	if err := os.Symlink(target, path+".new"); err != nil {
		return false, err
	}
	return true, os.Rename(path+".new", path)
}

// deployed is the room that asked for a release, kept in dir until the host
// boots into it.
type deployed struct {
	SHA  string `json:"sha"`
	Room string `json:"room"`
}

// Deploy builds the garage at rev, which must be on ws's default branch, and
// releases it through the bucket, remembering room to tell on boot. The build
// is the garage's own, in a fresh worktree: go test ./..., then a linux/amd64
// go build of ./cmd/garage. It returns the full sha.
func Deploy(ctx context.Context, c *bucket.Caller, dir string, ws *workspace.Workspace, rev, room string) (string, error) {
	sha, err := ws.Merged(ctx, rev)
	if err != nil {
		return "", err
	}
	if Running(dir) == sha {
		return "", fmt.Errorf("the garage is running %s already", sha)
	}
	bin, err := build(ctx, ws, sha)
	if err != nil {
		return "", err
	}
	notice := filepath.Join(dir, "deployed")
	raw, _ := json.Marshal(deployed{SHA: sha, Room: room})
	if err := os.WriteFile(notice, raw, 0o644); err != nil {
		return "", err
	}
	if err := Publish(ctx, c, sha, bin); err != nil {
		os.Remove(notice)
		return "", err
	}
	return sha, nil
}

func build(ctx context.Context, ws *workspace.Workspace, sha string) ([]byte, error) {
	id := "release-" + sha[:12] + "-" + time.Now().UTC().Format("20060102-150405")
	wt, err := ws.Checkout(ctx, id, workspace.PullRequest{Head: sha, HeadSHA: sha})
	if err != nil {
		return nil, err
	}
	defer wt.Close()
	for _, cmd := range [][]string{
		{"go", "test", "./..."},
		{"env", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0", "go", "build", "-o", "garage-release", "./cmd/garage"},
	} {
		res, err := wt.Sandbox().Exec(ctx, agent.Command{Cmd: cmd[0], Args: cmd[1:]})
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			out := res.Stdout + res.Stderr
			return nil, fmt.Errorf("%s failed at %s:\n%s", strings.Join(cmd, " "), sha[:12], out[max(0, len(out)-3000):])
		}
	}
	return os.ReadFile(filepath.Join(wt.Dir(), "garage-release"))
}

// Booted is the room that deployed the release the host has just started,
// and what to tell it; "" means nobody is waiting to hear. Each deploy is
// told once.
func Booted(dir string) (room, text string) {
	notice := filepath.Join(dir, "deployed")
	raw, err := os.ReadFile(notice)
	if err != nil {
		return "", ""
	}
	os.Remove(notice)
	var d deployed
	if json.Unmarshal(raw, &d) != nil || d.Room == "" {
		return "", ""
	}
	running := Running(dir)
	return d.Room, "running " + running[:min(len(running), 12)]
}
