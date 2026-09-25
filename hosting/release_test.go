package hosting_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/hosting"
	"github.com/maikdotfi/agentgarage/workspace"
)

// sides are a laptop and a host sharing a fake bucket, each trusting the
// other's key, as garage init and garage setup leave them.
type sides struct {
	store        *bucket.Fake
	laptop, host *bucket.Caller
	hostKey      ed25519.PrivateKey
}

func newSides(t *testing.T) sides {
	t.Helper()
	_, lkey, _ := ed25519.GenerateKey(nil)
	_, hkey, _ := ed25519.GenerateKey(nil)
	store := bucket.NewFake()
	limits := map[string]bucket.Limit{"release": {Every: time.Millisecond, Burst: 100}}
	return sides{
		store:   store,
		laptop:  bucket.New(store, lkey, []ed25519.PublicKey{hkey.Public().(ed25519.PublicKey)}, limits).Caller("release"),
		host:    bucket.New(store, hkey, []ed25519.PublicKey{lkey.Public().(ed25519.PublicKey)}, limits).Caller("release"),
		hostKey: hkey,
	}
}

// program is a stand-in garage binary: it runs, and says which it is.
func program(name string) []byte { return []byte("#!/bin/sh\necho " + name + "\n") }

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// releases is a host's releases dir whose poller has looked once already.
func releases(t *testing.T, host *bucket.Caller) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "releases")
	if sha, err := hosting.Update(context.Background(), host, dir); err != nil || sha != "" {
		t.Fatalf("first look = %q, %v", sha, err)
	}
	return dir
}

// runs is what the host's current release prints.
func runs(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command(filepath.Join(dir, "current")).Output()
	if err != nil {
		t.Fatalf("current release: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestAReleaseFromTheLaptopIsInstalledOnTheHost(t *testing.T) {
	ctx := context.Background()
	s := newSides(t)
	dir := releases(t, s.host)

	if err := hosting.Publish(ctx, s.laptop, shaA, program("a")); err != nil {
		t.Fatal(err)
	}
	sha, err := hosting.Update(ctx, s.host, dir)
	if err != nil || sha != shaA {
		t.Fatalf("Update = %q, %v; want %s installed", sha, err, shaA)
	}
	if got := runs(t, dir); got != "a" {
		t.Errorf("the host runs %q, want release a", got)
	}
	if got := hosting.Running(dir); got != shaA {
		t.Errorf("Running = %q", got)
	}

	before := s.store.Counts()
	if sha, err := hosting.Update(ctx, s.host, dir); err != nil || sha != "" {
		t.Errorf("a second look = %q, %v; want nothing new", sha, err)
	}
	if got := s.store.Counts(); got.ClassB-before.ClassB != 1 || got.ClassA != before.ClassA {
		t.Errorf("a look with nothing new cost %+v, want one GET", got)
	}
}

func TestPointingCurrentBackRollsBackWithoutADownload(t *testing.T) {
	ctx := context.Background()
	s := newSides(t)
	dir := releases(t, s.host)
	hosting.Publish(ctx, s.laptop, shaA, program("a"))
	hosting.Update(ctx, s.host, dir)
	hosting.Publish(ctx, s.laptop, shaB, program("b"))
	hosting.Update(ctx, s.host, dir)

	if err := hosting.Point(ctx, s.laptop, shaA); err != nil {
		t.Fatal(err)
	}
	before := s.store.Counts()
	if sha, err := hosting.Update(ctx, s.host, dir); err != nil || sha != shaA {
		t.Fatalf("Update = %q, %v", sha, err)
	}
	if got := runs(t, dir); got != "a" {
		t.Errorf("after pointing back the host runs %q", got)
	}
	if got := s.store.Counts(); got.ClassB-before.ClassB != 1 {
		t.Errorf("rolling back to an installed release took %d GETs, want only the pointer", got.ClassB-before.ClassB)
	}
	if err := hosting.Point(ctx, s.laptop, "cccccccccccccccccccccccccccccccccccccccc"); err == nil {
		t.Error("pointed current at a release that was never published")
	}
}

func TestTheFirstLookOnlyRemembers(t *testing.T) {
	ctx := context.Background()
	s := newSides(t)
	hosting.Publish(ctx, s.laptop, shaA, program("a"))
	dir := filepath.Join(t.TempDir(), "releases")

	// garage setup installed some binary by hand; the poller leaves it be
	// until releases/current moves.
	if sha, err := hosting.Update(ctx, s.host, dir); err != nil || sha != "" {
		t.Errorf("the first look installed %q, %v", sha, err)
	}
	hosting.Publish(ctx, s.laptop, shaB, program("b"))
	if sha, _ := hosting.Update(ctx, s.host, dir); sha != shaB {
		t.Errorf("the next release installed %q, want b", sha)
	}
}

func TestReleasesTheHostCannotTrustAreNotInstalled(t *testing.T) {
	ctx := context.Background()
	s := newSides(t)
	dir := releases(t, s.host)
	hosting.Publish(ctx, s.laptop, shaA, program("a"))
	hosting.Update(ctx, s.host, dir)
	_, rogueKey, _ := ed25519.GenerateKey(nil)
	rogue := bucket.New(s.store, rogueKey, nil, map[string]bucket.Limit{"r": {Every: time.Millisecond, Burst: 10}}).Caller("r")

	// Someone with the bucket token swaps in their own binary under a real release.
	hosting.Publish(ctx, s.laptop, shaB, program("b"))
	rogue.Put(ctx, "releases/"+shaB+"/garage", program("evil"))
	if _, err := hosting.Update(ctx, s.host, dir); !errors.Is(err, bucket.ErrBadSignature) {
		t.Errorf("Update = %v, want a bad signature", err)
	}
	if got := runs(t, dir); got != "a" {
		t.Errorf("the host runs %q, want a still", got)
	}
}

func TestABinaryThatDoesNotRunHereIsNotInstalled(t *testing.T) {
	ctx := context.Background()
	s := newSides(t)
	dir := releases(t, s.host)
	hosting.Publish(ctx, s.laptop, shaA, program("a"))
	hosting.Update(ctx, s.host, dir)

	hosting.Publish(ctx, s.laptop, shaB, []byte("\xcf\xfa\xed\xfe a binary for another machine"))
	if sha, err := hosting.Update(ctx, s.host, dir); err == nil {
		t.Errorf("installed %q, want it refused", sha)
	}
	before := s.store.Counts()
	if sha, err := hosting.Update(ctx, s.host, dir); sha != "" || err != nil {
		t.Errorf("the next look = %q, %v; want the refused release left alone", sha, err)
	}
	if got := s.store.Counts(); got.ClassB-before.ClassB != 1 {
		t.Errorf("the next look took %d GETs, want only the pointer, not the binary again", got.ClassB-before.ClassB)
	}
	if got := runs(t, dir); got != "a" {
		t.Errorf("the host runs %q, want a still", got)
	}
}

// garageRepo is a bare repo standing in for this one: a main that builds and
// has a test, and a branch whose test fails.
func garageRepo(t *testing.T) (bare, main, broken string) {
	t.Helper()
	dir := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	bare = filepath.Join(dir, "origin.git")
	git(dir, "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(dir, "seed")
	git(dir, "clone", "-q", bare, seed)
	write := func(path, content string) {
		os.MkdirAll(filepath.Dir(filepath.Join(seed, path)), 0o755)
		os.WriteFile(filepath.Join(seed, path), []byte(content), 0o644)
	}
	write("go.mod", "module example.com/garage\n\ngo 1.24\n")
	write("cmd/garage/main.go", "package main\n\nfunc main() { println(\"usage\") }\n")
	write("cmd/garage/main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")
	git(seed, "add", ".")
	git(seed, "commit", "-qm", "garage")
	git(seed, "push", "-q", "origin", "HEAD:main")
	main = git(seed, "rev-parse", "HEAD")
	git(seed, "checkout", "-qb", "broken")
	write("cmd/garage/main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) { t.Fatal(\"broken\") }\n")
	git(seed, "commit", "-qam", "break the test")
	git(seed, "push", "-q", "origin", "broken")
	return bare, main, git(seed, "rev-parse", "HEAD")
}

func TestDeployReleasesOnlyWhatIsMergedAndPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go program")
	}
	ctx := context.Background()
	s := newSides(t)
	dir := releases(t, s.host)
	bare, main, broken := garageRepo(t)
	mise := filepath.Join(t.TempDir(), "mise")
	os.WriteFile(mise, []byte("#!/bin/sh\nif [ \"$1\" = exec ]; then shift 2; exec \"$@\"; fi\n"), 0o755)
	ws, err := workspace.Open(ctx, workspace.Config{Name: "agentgarage", Remote: bare, Root: t.TempDir(), Mise: mise})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := hosting.Deploy(ctx, s.host, dir, ws, broken, "ship"); err == nil || !strings.Contains(err.Error(), "not on main") {
		t.Errorf("deploying a commit off main = %v, want it refused", err)
	}
	git := exec.Command("git", "update-ref", "refs/heads/main", broken) // a human merged it
	git.Dir = bare
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := hosting.Deploy(ctx, s.host, dir, ws, broken[:7], "ship"); err == nil || !strings.Contains(err.Error(), "go test") {
		t.Errorf("deploying a commit whose tests fail = %v, want it refused", err)
	}
	if _, err := s.laptop.Get(ctx, "releases/current"); !errors.Is(err, bucket.ErrNotFound) {
		t.Fatalf("a refused deploy moved releases/current: %v", err)
	}

	sha, err := hosting.Deploy(ctx, s.host, dir, ws, main[:10], "ship")
	if err != nil || sha != main {
		t.Fatalf("Deploy = %q, %v; want %s released", sha, err, main)
	}
	bin, err := s.laptop.Get(ctx, "releases/"+main+"/garage")
	if err != nil || !strings.HasPrefix(string(bin.Body), "\x7fELF") {
		t.Errorf("the release is not a Linux binary signed by the host: %v", err)
	}
	if cur, _ := s.laptop.Get(ctx, "releases/current"); string(cur.Body) != main {
		t.Errorf("releases/current = %q, want %s", cur.Body, main)
	}

	// The Linux binary can't run here, so a script stands in for it.
	s.host.Put(ctx, "releases/"+main+"/garage", program("new"))
	if got, err := hosting.Update(ctx, s.host, dir); err != nil || got != main {
		t.Fatalf("Update = %q, %v", got, err)
	}
	if room, text := hosting.Booted(dir); room != "ship" || !strings.Contains(text, "running "+main[:12]) {
		t.Errorf("on boot: %q in %q, want the release announced to ship", text, room)
	}
	if room, _ := hosting.Booted(dir); room != "" {
		t.Error("a second boot announced the deploy again")
	}
	if _, err := hosting.Deploy(ctx, s.host, dir, ws, main, "ship"); err == nil || !strings.Contains(err.Error(), "running") {
		t.Errorf("deploying what runs already = %v, want it refused", err)
	}
}

func TestALaptopReleaseIsNotAnnounced(t *testing.T) {
	ctx := context.Background()
	s := newSides(t)
	dir := releases(t, s.host)
	hosting.Publish(ctx, s.laptop, shaA, program("a"))
	hosting.Update(ctx, s.host, dir)
	if room, _ := hosting.Booted(dir); room != "" {
		t.Errorf("a laptop release was announced in %q; nobody deployed it from a room", room)
	}
}
