package agents_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maikdotfi/agentgarage/agents"
	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/metaharness/agentdb/turso"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/metaharness/testutils"
	"github.com/maikdotfi/agentgarage/workspace"
)

func newStore(t *testing.T) agents.Store {
	t.Helper()
	return openStore(t, ":memory:")
}

func openStore(t *testing.T, path string) *turso.Store {
	t.Helper()
	s, err := turso.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// garage is the part of garage serve an agent lives in: a chatroom and one
// agent database, both files in dir, so a test can stop it and start another.
type garage struct {
	chat  *chatroom.Service
	store *turso.Store
}

func start(t *testing.T, dir string) garage {
	t.Helper()
	chat, err := chatroom.Open(context.Background(), filepath.Join(dir, "chatroom.db"))
	if err != nil {
		t.Fatal(err)
	}
	return garage{chat: chat, store: openStore(t, filepath.Join(dir, "agent.db"))}
}

func (g garage) stop() {
	g.chat.Close()
	g.store.Close()
}

func TestDevPicksUpItsTaskAfterARestart(t *testing.T) {
	ws, _, _ := newWorkspace(t, "demo")
	dir := t.TempDir()
	m := &testutils.ScriptedModel{Replies: []model.Message{
		testutils.AssistantToolCall(t, "1", "bash", map[string]string{"cmd": "echo one > one.txt"}),
		testutils.AssistantText("wrote one"),
		testutils.AssistantToolCall(t, "2", "bash", map[string]string{"cmd": "cat one.txt && git branch --show-current"}),
		testutils.AssistantText("still here"),
	}}
	join := func(g garage) {
		g.chat.Join("dev", agents.Dev(agents.DevConfig{Chat: g.chat, Model: m, ModelID: "x", Store: g.store, Workspaces: []*workspace.Workspace{ws}}))
	}

	before := start(t, dir)
	join(before)
	ask := post(t, before.chat, "task", "mike", "@dev write one.txt")
	replyFrom(t, before.chat, "task", "dev", ask.ID)
	before.stop()

	after := start(t, dir)
	defer after.stop()
	join(after)
	again := post(t, after.chat, "task", "mike", "@dev now read it back")
	replyFrom(t, after.chat, "task", "dev", again.ID)

	second := userText(m.Calls[2])
	for _, want := range []string{"@dev write one.txt", "wrote one", "@dev now read it back"} {
		if !strings.Contains(second, want) {
			t.Errorf("the turn after the restart is missing %q from the session:\n%s", want, second)
		}
	}
	if n := strings.Count(second, "@dev write one.txt"); n != 1 {
		t.Errorf("the first ask is in the session %d times, want once", n)
	}
	if got := lastToolResult(m.Calls[3]); !strings.Contains(got, "one") || !strings.Contains(got, "garage/task-") {
		t.Errorf("after the restart dev ran in %q, want the same worktree and branch", got)
	}
}

func TestGrugRemembersWhatItReviewedAfterARestart(t *testing.T) {
	ws, _, _ := newWorkspace(t, "demo")
	dir := t.TempDir()
	m := &testutils.ScriptedModel{Replies: []model.Message{testutils.AssistantText("review one")}}
	openedPR(t, ws)
	const ask = "@grug review https://github.com/example/demo/pull/1"
	join := func(g garage) {
		g.chat.Join("grug", agents.Grug(agents.GrugConfig{Chat: g.chat, Model: m, ModelID: "x", Store: g.store, Workspaces: []*workspace.Workspace{ws}}))
	}

	before := start(t, dir)
	join(before)
	replyFrom(t, before.chat, "r", "grug", post(t, before.chat, "r", "dev", ask).ID)
	before.stop()

	after := start(t, dir)
	defer after.stop()
	join(after)
	again := replyFrom(t, after.chat, "r", "grug", post(t, after.chat, "r", "dev", ask).ID)
	if !strings.Contains(again.Text, "already reviewed") || len(m.Calls) != 1 {
		t.Errorf("after a restart grug said %q and ran the model %d times, want it to remember the head", again.Text, len(m.Calls))
	}
}
