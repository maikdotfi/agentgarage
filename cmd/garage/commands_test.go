package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maikdotfi/agentgarage/chatroom"
)

// home is a fresh GARAGE_HOME short enough for a unix socket path.
func home(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "garage")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("GARAGE_HOME", dir)
	return dir
}

func garage(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestInitMakesKeysOnceAndSaysWhatToShare(t *testing.T) {
	dir := home(t)
	code, out, errOut := garage(t, "", "init", "-master")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, f := range []string{"signing.key", "master.key", "recipient"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("init did not write %s: %v", f, err)
		}
	}
	recipient, _ := os.ReadFile(filepath.Join(dir, "recipient"))
	if !strings.Contains(out, strings.TrimSpace(string(recipient))) || !strings.Contains(out, "public key") {
		t.Errorf("output does not show the public key and recipient:\n%s", out)
	}

	if code, _, _ := garage(t, "", "init", "-master"); code == 0 {
		t.Error("a second init succeeded; it must never overwrite keys")
	}
}

func TestChatPostsOverTheSocketAndShowsTheRoom(t *testing.T) {
	dir := home(t)
	chat, err := chatroom.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer chat.Close()
	chat.Post(context.Background(), "fix", "dev", "ready when you are")
	ln, err := net.Listen("unix", filepath.Join(dir, "garage.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: chat.Handler()}
	go srv.Serve(ln)
	defer srv.Close()

	code, out, errOut := garage(t, "@dev fix it please\n", "chat", "-room", "fix", "-as", "mike")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "dev: ready when you are") {
		t.Errorf("chat did not show the room's history:\n%s", out)
	}
	msgs, _ := chat.Read(context.Background(), "fix", 0)
	if last := msgs[len(msgs)-1]; last.Author != "mike" || last.Text != "@dev fix it please" {
		t.Errorf("last message = %+v", last)
	}
}

func TestChatWithoutServeSaysSo(t *testing.T) {
	home(t)
	code, _, errOut := garage(t, "", "chat")
	if code == 0 || !strings.Contains(errOut, "garage serve") {
		t.Errorf("exit %d, stderr %q; want a hint that serve is not running", code, errOut)
	}
}

func TestServeWithoutABucketSaysWhatIsMissing(t *testing.T) {
	home(t)
	t.Setenv("GARAGE_R2_ENDPOINT", "")
	code, _, errOut := garage(t, "", "serve", "-http", "127.0.0.1:0") // never the real :8080, which a host may be using
	if code == 0 || !strings.Contains(errOut, "GARAGE_R2_ENDPOINT") || !strings.Contains(errOut, "r2.env") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

func TestTheBucketCanComeFromR2Env(t *testing.T) {
	dir := home(t)
	for _, v := range []string{"GARAGE_R2_ENDPOINT", "GARAGE_R2_BUCKET", "GARAGE_R2_ACCESS_KEY_ID", "GARAGE_R2_SECRET_ACCESS_KEY"} {
		t.Setenv(v, "")
	}
	garage(t, "", "init", "-master")
	empty := httptest.NewServer(http.NotFoundHandler()) // a bucket with nothing in it
	defer empty.Close()
	os.WriteFile(filepath.Join(dir, "r2.env"), []byte("# the bucket\nGARAGE_R2_ENDPOINT="+empty.URL+
		"\nGARAGE_R2_BUCKET=garage\nGARAGE_R2_ACCESS_KEY_ID=id\nGARAGE_R2_SECRET_ACCESS_KEY=secret\n"), 0o600)

	code, _, errOut := garage(t, "", "serve", "-http", "127.0.0.1:0") // never the real :8080, which a host may be using
	if code == 0 || !strings.Contains(errOut, "garage remote config") {
		t.Errorf("exit %d, stderr %q; want serve to reach the bucket and find no config", code, errOut)
	}
}

func TestSetupTakesARangeToSSHFrom(t *testing.T) {
	home(t)
	code, _, errOut := garage(t, "", "setup", "-ssh-from", "192.168.100.0/24", "-trust", "abc")
	if code == 2 || strings.Contains(errOut, "-ssh-from") {
		t.Errorf("exit %d, stderr %q; want the range accepted", code, errOut)
	}
}

func TestSetupNeedsTheOneIPThatMaySSHIn(t *testing.T) {
	home(t)
	code, _, errOut := garage(t, "", "setup", "-trust", "abc")
	if code != 2 || !strings.Contains(errOut, "-ssh-from") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}
