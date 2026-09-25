package agents_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/agents"
	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/metaharness/testutils"
	"github.com/maikdotfi/agentgarage/workspace"
)

func git(t *testing.T, dir string, args ...string) string {
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

// newWorkspace is a workspace over a local bare repo, with a fake gh that
// logs its calls to ghLog, and a mise that just runs what it is asked to exec.
// The gh opens one PR, and pr view shows it with the head origin has.
func newWorkspace(t *testing.T, name string) (ws *workspace.Workspace, origin, ghLog string) {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	git(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(dir, "seed")
	git(t, dir, "clone", "-q", bare, seed)
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-qm", "first")
	git(t, seed, "push", "-q", "origin", "HEAD:main")

	gh, ghLog, state := filepath.Join(dir, "gh"), filepath.Join(dir, "gh.log"), filepath.Join(dir, "head")
	url := "https://github.com/example/" + name + "/pull/1"
	os.WriteFile(gh, []byte(`#!/bin/sh
printf '%s\n' "$@" >> `+ghLog+`
case "$1 $2" in
"pr create")
  while [ $# -gt 0 ]; do [ "$1" = --head ] && echo "$2" > `+state+`; shift; done
  echo `+url+` ;;
"pr view")
  [ -f `+state+` ] || { echo 'no pull requests found' >&2; exit 1; }
  head=$(cat `+state+`)
  sha=$(git ls-remote origin "refs/heads/$head" | cut -f1)
  printf '{"url":"`+url+`","state":"OPEN","headRefName":"%s","headRefOid":"%s","baseRefName":"main"}\n' "$head" "$sha" ;;
esac
`), 0o755)
	mise := filepath.Join(dir, "mise")
	os.WriteFile(mise, []byte("#!/bin/sh\nif [ \"$1\" = exec ]; then shift 2; exec \"$@\"; fi\n"), 0o755)
	ws, err := workspace.Open(context.Background(), workspace.Config{
		Name: name, Remote: bare, Root: t.TempDir(), GH: gh, Mise: mise,
		Identity: workspace.Identity{Name: "dev", Email: "dev@garage.invalid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ws, bare, ghLog
}

func newChat(t *testing.T) *chatroom.Service {
	t.Helper()
	chat, err := chatroom.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Close() })
	return chat
}

// replyFrom waits for the next message by author in room after the message with ID after.
func replyFrom(t *testing.T, chat *chatroom.Service, room, author string, after int64) chatroom.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		msgs, err := chat.Wait(ctx, room, after)
		if err != nil {
			t.Fatalf("waiting for %s in %s: %v", author, room, err)
		}
		for _, m := range msgs {
			if m.Author == author {
				return m
			}
			after = m.ID
		}
	}
}

func userText(req model.ModelRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, p := range model.TextParts(&m) {
			b.WriteString(p.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestDevOpensAPRWhenAskedInARoom(t *testing.T) {
	ws, origin, _ := newWorkspace(t, "demo")
	chat := newChat(t)
	m := &testutils.ScriptedModel{Replies: []model.Message{
		testutils.AssistantToolCall(t, "1", "bash", map[string]string{"cmd": "echo hi > hello.txt && git add hello.txt && git commit -qm 'add hello'"}),
		testutils.AssistantToolCall(t, "2", "open_pull_request", map[string]string{"title": "Add hello", "body": "Says hi."}),
		testutils.AssistantText("Done: https://github.com/example/demo/pull/1"),
	}}
	chat.Join("dev", agents.Dev(agents.DevConfig{Chat: chat, Model: m, ModelID: "test-model", Store: newStore(t), Workspaces: []*workspace.Workspace{ws}}))

	ask := post(t, chat, "hello", "mike", "@dev add hello.txt in demo")

	pr := replyFrom(t, chat, "hello", "dev", ask.ID)
	if !strings.Contains(pr.Text, "https://github.com/example/demo/pull/1") {
		t.Errorf("dev's first reply = %q, want the PR", pr.Text)
	}
	replyFrom(t, chat, "hello", "dev", pr.ID)

	if got := git(t, origin, "log", "-1", "--format=%s", branchOf(t, origin)); got != "add hello" {
		t.Errorf("pushed branch is at %q", got)
	}
	if got := git(t, origin, "log", "-1", "--format=%s", "main"); got != "first" {
		t.Errorf("main moved to %q", got)
	}
	first := userText(m.Calls[0])
	if !strings.Contains(first, "mike: @dev add hello.txt in demo") || !strings.Contains(first, "demo") {
		t.Errorf("first prompt does not carry the ask and workspace:\n%s", first)
	}
	if m.Calls[0].Model != "test-model" {
		t.Errorf("model = %q", m.Calls[0].Model)
	}
}

func TestDevContinuesTheSameTaskInTheSameRoom(t *testing.T) {
	ws, _, _ := newWorkspace(t, "demo")
	chat := newChat(t)
	m := &testutils.ScriptedModel{Replies: []model.Message{
		testutils.AssistantToolCall(t, "1", "bash", map[string]string{"cmd": "echo one > one.txt"}),
		testutils.AssistantText("wrote one"),
		testutils.AssistantToolCall(t, "2", "bash", map[string]string{"cmd": "cat one.txt"}),
		testutils.AssistantText("still here"),
	}}
	chat.Join("dev", agents.Dev(agents.DevConfig{Chat: chat, Model: m, ModelID: "x", Store: newStore(t), Workspaces: []*workspace.Workspace{ws}}))

	ask := post(t, chat, "task", "mike", "@dev write one.txt")
	replyFrom(t, chat, "task", "dev", ask.ID)
	post(t, chat, "task", "sam", "looks good")
	again := post(t, chat, "task", "mike", "@dev now read it back")
	replyFrom(t, chat, "task", "dev", again.ID)

	second := userText(m.Calls[2])
	for _, want := range []string{"wrote one", "sam: looks good", "mike: @dev now read it back"} {
		if !strings.Contains(second, want) {
			t.Errorf("follow-up turn is missing %q:\n%s", want, second)
		}
	}
	if !strings.Contains(lastToolResult(m.Calls[3]), "one") {
		t.Errorf("follow-up did not run in the same worktree")
	}
}

func TestDevAsksWhichWorkspaceWhenItCannotTell(t *testing.T) {
	a, _, _ := newWorkspace(t, "alpha")
	b, _, _ := newWorkspace(t, "beta")
	chat := newChat(t)
	m := &testutils.ScriptedModel{}
	chat.Join("dev", agents.Dev(agents.DevConfig{Chat: chat, Model: m, ModelID: "x", Store: newStore(t), Workspaces: []*workspace.Workspace{a, b}}))

	ask := post(t, chat, "r", "mike", "@dev fix the bug")
	reply := replyFrom(t, chat, "r", "dev", ask.ID)
	if !strings.Contains(reply.Text, "alpha") || !strings.Contains(reply.Text, "beta") {
		t.Errorf("reply = %q, want it to name the workspaces", reply.Text)
	}
	if len(m.Calls) != 0 {
		t.Errorf("the model was called %d times before a workspace was chosen", len(m.Calls))
	}
}

func post(t *testing.T, chat *chatroom.Service, room, author, text string) chatroom.Message {
	t.Helper()
	msg, err := chat.Post(context.Background(), room, author, text)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// branchOf is the one garage/ branch in the bare repo.
func branchOf(t *testing.T, origin string) string {
	t.Helper()
	return git(t, origin, "branch", "--list", "garage/*", "--format=%(refname:short)")
}

func lastToolResult(req model.ModelRequest) string {
	last := req.Messages[len(req.Messages)-1]
	for _, r := range model.ToolResults(&last) {
		return r.Text
	}
	return ""
}
