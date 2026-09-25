package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/mail"
	"github.com/maikdotfi/agentgarage/bucket/secrets"
	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/hosting"
	"github.com/maikdotfi/agentgarage/metaharness/model"
)

// origin is a local bare repo with one commit on main.
func origin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bare := filepath.Join(dir, "origin.git")
	git(dir, "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(dir, "seed")
	git(dir, "clone", "-q", bare, seed)
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644)
	git(seed, "add", ".")
	git(seed, "commit", "-qm", "first")
	git(seed, "push", "-q", "origin", "HEAD:main")
	return bare
}

// garageHost is a host home made by garage init -master, trusting a laptop
// that shares its fake bucket.
type garageHost struct {
	home     string
	ui       string // where serve's chat UI listens, once it runs
	store    *bucket.Fake
	laptop   *bucket.Caller
	releases string            // serve's releases dir; "" is off a host
	model    model.ModelClient // nil is the configured provider
}

func newHost(t *testing.T) *garageHost {
	t.Helper()
	dir := home(t)
	if code, _, errOut := garage(t, "", "init", "-master"); code != 0 {
		t.Fatal(errOut)
	}
	hostKey, err := bucket.LoadKey(filepath.Join(dir, signingKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	lpub, lpriv, _ := ed25519.GenerateKey(nil)
	os.WriteFile(filepath.Join(dir, trustedFile), []byte(base64.StdEncoding.EncodeToString(lpub)+"\n"), 0o644)
	store := bucket.NewFake()
	laptop := bucket.New(store, lpriv, []ed25519.PublicKey{hostKey.Public().(ed25519.PublicKey)},
		map[string]bucket.Limit{"laptop": {Every: time.Millisecond, Burst: 100}}).Caller("laptop")
	return &garageHost{home: dir, store: store, laptop: laptop}
}

// configure writes the secrets and the config a laptop would.
func (h *garageHost) configure(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	recipient, _ := os.ReadFile(filepath.Join(h.home, recipientFile))
	for _, name := range []string{"GH_TOKEN", "ANTHROPIC_API_KEY"} {
		if err := secrets.Put(ctx, h.laptop, strings.TrimSpace(string(recipient)), name, "test-"+name); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `{"workspaces": {"demo": "` + origin(t) + `"}}`
	if _, err := h.laptop.Put(ctx, configKey, []byte(cfg)); err != nil {
		t.Fatal(err)
	}
}

// serve runs garage serve on the fake bucket until the test ends, and waits
// for its socket.
func (h *garageHost) serve(t *testing.T) {
	t.Helper()
	done, stop := h.start(t)
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
}

// start runs garage serve on the fake bucket, and waits for its socket. The
// caller stops it and reads how it ended.
func (h *garageHost) start(t *testing.T) (<-chan error, context.CancelFunc) {
	t.Helper()
	ui, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.ui = "http://" + ui.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, serveEnv{home: h.home, keys: h.home, store: h.store, ui: ui, mailEvery: 10 * time.Millisecond,
			releases: h.releases, model: h.model})
	}()
	sock := filepath.Join(h.home, socketFile)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("serve stopped: %v", err)
		default:
		}
		if _, err := socketClient(sock).read(ctx, "garage", 0, ""); err == nil {
			return done, cancel
		}
	}
	cancel()
	t.Fatal("serve never answered on its socket")
	return nil, nil
}

// waitFor reads room over the socket until a message has text.
func (h *garageHost) waitFor(t *testing.T, room, text string) {
	t.Helper()
	c := socketClient(filepath.Join(h.home, socketFile))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var last int64
	for ctx.Err() == nil {
		msgs, _ := c.read(ctx, room, last, "1s")
		for _, m := range msgs {
			if m.Text == text {
				return
			}
			last = m.ID
		}
	}
	t.Fatalf("%q never appeared in %s", text, room)
}

func TestServeTakesMailFromTheLaptop(t *testing.T) {
	h := newHost(t)
	h.configure(t)
	h.serve(t)

	mail.Send(context.Background(), h.laptop, mail.ToHost, 0, mail.Message{Room: "fix", Author: "mike", Text: "hello from the laptop"})

	h.waitFor(t, "fix", "hello from the laptop")
}

func TestServeShowsTheChatUIOverHTTP(t *testing.T) {
	h := newHost(t)
	h.configure(t)
	h.serve(t)

	resp, err := http.PostForm(h.ui+"/rooms/fix/messages", url.Values{"as": {"mike"}, "text": {"hello from the browser"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "hello from the browser") {
		t.Errorf("status %d, want the room with the new message:\n%s", resp.StatusCode, body)
	}
	h.waitFor(t, "fix", "hello from the browser")
}

func TestServeStopsPromptlyWithARoomStreamOpen(t *testing.T) {
	var stopping time.Time
	var stream io.Closer
	t.Cleanup(func() { // runs last, after serve has stopped
		if d := time.Since(stopping); d > 2*time.Second {
			t.Errorf("serve took %v to stop with a stream open", d)
		}
		stream.Close()
	})
	h := newHost(t)
	h.configure(t)
	h.serve(t)
	t.Cleanup(func() { stopping = time.Now() }) // runs first, before serve stops

	resp, err := http.Get(h.ui + "/rooms/fix/events")
	if err != nil {
		t.Fatal(err)
	}
	stream = resp.Body
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
}

func TestBackupSnapshotsEveryDatabase(t *testing.T) {
	h := newHost(t)
	h.configure(t)
	h.serve(t)

	code, out, errOut := garage(t, "", "backup")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, owner := range []string{"garage/chatroom", "agents/dev", "agents/grug"} {
		if _, err := h.laptop.Get(context.Background(), owner+"/db/latest"); err != nil {
			t.Errorf("%s: %v", owner, err)
		}
		if !strings.Contains(out, owner+"/db/") {
			t.Errorf("output does not name the %s snapshot:\n%s", owner, out)
		}
	}
}

func TestServeOnAnEmptyDiskRestoresTheLatestSnapshots(t *testing.T) {
	ctx := context.Background()
	h := newHost(t)
	h.configure(t)
	// Yesterday's host, with the same key, left a chatroom in the bucket.
	old, err := chatroom.Open(ctx, filepath.Join(t.TempDir(), "chatroom.db"))
	if err != nil {
		t.Fatal(err)
	}
	old.Post(ctx, "fix", "mike", "from before the rebuild")
	key, _ := bucket.LoadKey(filepath.Join(h.home, signingKeyFile))
	oldHost := bucket.New(h.store, key, nil, map[string]bucket.Limit{"backup": {Every: time.Millisecond, Burst: 10}})
	if _, err := hosting.Backup(ctx, oldHost.Caller("backup"), "2026-09-23", map[string]hosting.Snapshot{"garage/chatroom": old.Snapshot}); err != nil {
		t.Fatal(err)
	}
	old.Close()

	h.serve(t)

	h.waitFor(t, "fix", "from before the rebuild")
}

func TestServeWithoutConfigSaysHowToWriteIt(t *testing.T) {
	h := newHost(t)
	err := runServe(context.Background(), serveEnv{home: h.home, keys: h.home, store: h.store, mailEvery: time.Second})
	if err == nil || !strings.Contains(err.Error(), "garage remote config") {
		t.Errorf("err = %v, want a hint to run garage remote config", err)
	}
}
