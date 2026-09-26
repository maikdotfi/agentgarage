package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"

	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/hosting"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/metaharness/testutils"
)

// slowModel answers once it is let go, so a test can hold an agent mid-turn.
type slowModel struct{ letGo chan struct{} }

func (m slowModel) Generate(ctx context.Context, _ model.ModelRequest) (fantasy.Message, fantasy.Usage, error) {
	select {
	case <-m.letGo:
		return testutils.AssistantText("done"), fantasy.Usage{}, nil
	case <-ctx.Done():
		return fantasy.Message{}, fantasy.Usage{}, ctx.Err()
	}
}

func (m slowModel) Stream(ctx context.Context, req model.ModelRequest) (fantasy.StreamResponse, error) {
	return model.Streamed(m.Generate(ctx, req))
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

const releaseSHA = "0123456789abcdef0123456789abcdef01234567"

func TestServeRestartsIntoANewReleaseOnceTheAgentsAreIdle(t *testing.T) {
	ctx := context.Background()
	h := newHost(t)
	h.configure(t)
	h.releases = filepath.Join(t.TempDir(), "releases")
	busy := slowModel{letGo: make(chan struct{})}
	h.model = busy
	done, stop := h.start(t)
	defer stop()
	waitForFile(t, filepath.Join(h.releases, "seen")) // the first look only remembers

	socketClient(filepath.Join(h.home, socketFile)).post(ctx, "task", "mike", "@dev take your time")
	if err := hosting.Publish(ctx, h.laptop, releaseSHA, []byte("#!/bin/sh\necho new\n")); err != nil {
		t.Fatal(err)
	}
	for hosting.Running(h.releases) != releaseSHA {
		select {
		case err := <-done:
			t.Fatalf("serve stopped before installing the release: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case err := <-done:
		t.Fatalf("serve stopped with dev mid-turn: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(busy.letGo)
	select {
	case err := <-done:
		if !errors.Is(err, errRestart) {
			t.Errorf("serve ended with %v, want a restart", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve never restarted into the new release")
	}
	chat, err := chatroom.Open(ctx, filepath.Join(h.home, "chatroom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer chat.Close()
	msgs, _ := chat.Read(ctx, "task", 0)
	if last := msgs[len(msgs)-1]; last.Author != "dev" || last.Text != "done" {
		t.Errorf("the room ends with %s: %q, want dev's answer from before the restart", last.Author, last.Text)
	}
}

func TestServeTellsTheRoomThatDeployedWhenItIsBack(t *testing.T) {
	h := newHost(t)
	h.configure(t)
	h.releases = filepath.Join(t.TempDir(), "releases")
	// What the release before this one left behind: it deployed from room
	// ship, installed the new release and exited.
	os.MkdirAll(filepath.Join(h.releases, releaseSHA), 0o755)
	os.WriteFile(filepath.Join(h.releases, releaseSHA, "garage"), []byte("#!/bin/sh\n"), 0o755)
	os.Symlink(releaseSHA+"/garage", filepath.Join(h.releases, "current"))
	os.WriteFile(filepath.Join(h.releases, "seen"), []byte(releaseSHA), 0o644)
	os.WriteFile(filepath.Join(h.releases, "deployed"), []byte(`{"sha":"`+releaseSHA+`","room":"ship"}`), 0o644)
	h.model = &testutils.ScriptedModel{Replies: []model.Message{testutils.AssistantText("checked it")}}

	h.serve(t)

	h.waitFor(t, "ship", "@dev running "+releaseSHA[:12])
	h.waitFor(t, "ship", "checked it")
}

// garageRepo is a git repo standing in for this one, with a garage to build.
func garageRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "cmd/garage"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/garage\n\ngo 1.24\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cmd/garage/main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-qm", "garage"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestRemoteReleaseShipsTheCommitThatIsCheckedOut(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go program")
	}
	ctx := context.Background()
	laptop, host, _ := mailPair(t)
	repo := garageRepo(t)
	head, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	sha := strings.TrimSpace(string(head))

	got, err := remoteRelease(ctx, laptop, repo)
	if err != nil || got != sha {
		t.Fatalf("remoteRelease = %q, %v; want %s", got, err, sha)
	}
	if cur, err := host.Get(ctx, "releases/current"); err != nil || string(cur.Body) != sha {
		t.Errorf("releases/current = %q, %v", cur.Body, err)
	}
	if bin, err := host.Get(ctx, "releases/"+sha+"/garage"); err != nil || !strings.HasPrefix(string(bin.Body), "\x7fELF") {
		t.Errorf("the host can't read a Linux binary from the laptop: %v", err)
	}

	os.WriteFile(filepath.Join(repo, "cmd/garage/main.go"), []byte("package main\n\nfunc main() { println() }\n"), 0o644)
	if _, err := remoteRelease(ctx, laptop, repo); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Errorf("releasing uncommitted work = %v, want it refused", err)
	}
	if cur, _ := host.Get(ctx, "releases/current"); string(cur.Body) != sha {
		t.Error("a refused release moved releases/current")
	}
}
