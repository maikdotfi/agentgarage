package agents_test

import (
	"context"
	"testing"

	"github.com/maikdotfi/agentgarage/agents"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/metaharness/testutils"
	"github.com/maikdotfi/agentgarage/workspace"
)

// TestRoomSessionsJoinsDevRoomsToTheirSessions: the observability page needs
// each room's session, which is what dev remembers under dev/rooms/<room>.
func TestRoomSessionsJoinsDevRoomsToTheirSessions(t *testing.T) {
	ws, _, _ := newWorkspace(t, "demo")
	chat := newChat(t)
	m := &testutils.ScriptedModel{Replies: []model.Message{testutils.AssistantText("done")}}
	store := newStore(t)
	chat.Join(agents.DevName, agents.Dev(agents.DevConfig{
		Chat: chat, Model: m, ModelID: "x", Store: store, Workspaces: []*workspace.Workspace{ws},
	}))

	ask := post(t, chat, "obs", "mike", "@dev hello")
	replyFrom(t, chat, "obs", agents.DevName, ask.ID)

	rooms, err := agents.RoomSessions(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 {
		t.Fatalf("RoomSessions() = %d rooms, want obs", len(rooms))
	}
	id := rooms["obs"]
	if id == "" {
		t.Fatal("the obs room has no session id")
	}
	sess, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("loading the session RoomSessions named: %v", err)
	}
	if sess.ID != id {
		t.Errorf("RoomSessions()[obs] = %q, want the room's session %q", id, sess.ID)
	}
}