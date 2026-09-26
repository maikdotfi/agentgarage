package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/metaharness/agent"
	"github.com/maikdotfi/agentgarage/metaharness/agentdb/turso"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/ui"
)

// observe is the UI on an in-memory chatroom, with two agents wired to it
// through in-memory turso stores holding the sessions the pages show.
func observe(t *testing.T) (*chatroom.Service, http.Handler) {
	t.Helper()
	chat, err := chatroom.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Close() })
	h, err := ui.New(chat, ui.Agent{Name: "dev", Model: "gpt:41", Store: agentStore(t, "dev")},
		ui.Agent{Name: "grug", Model: "gpt:41", Store: agentStore(t, "grug")})
	if err != nil {
		t.Fatal(err)
	}
	return chat, h
}

// agentStore is an in-memory agent database with a scripted session in it: the
// transcript the pages are tested against.
func agentStore(t *testing.T, name string) ui.AgentStore {
	t.Helper()
	ctx := context.Background()
	store, err := turso.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if name != "dev" {
		return store
	}

	// The silent deploy turn of the milestone: a tool call that failed, then a
	// message with reasoning and no text.
	sess := agent.NewSession("obs-20260926-064157", "gpt:41", nil)
	sess.Messages = []fantasy.Message{
		model.NewUserMessage("@dev deploy it again"),
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
			fantasy.ReasoningPart{Text: "the tests will pass this time"},
			fantasy.ToolCallPart{ToolCallID: "c1", ToolName: "bash", Input: `{"cmd":"go test ./..."}`},
		}},
		{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
			fantasy.ToolResultPart{ToolCallID: "c1", Output: fantasy.ToolResultOutputContentText{Text: "FAIL ui [build failed]\nvery long output follows"}},
		}},
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
			fantasy.ReasoningPart{Text: "the build broke on the host, not here"},
		}},
	}
	sess.Usage = fantasy.Usage{InputTokens: 100, OutputTokens: 40, TotalTokens: 140}
	sess.Status = agent.StatusActive
	if err := store.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"task": sess.ID})
	if err := store.Put(ctx, "dev/rooms/obs", raw); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAgentsPageShowsEachAgentAndWhereItsWorking(t *testing.T) {
	_, h := observe(t)

	code, body := get(t, h, "/agents")

	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, `href="/agents/dev"`, `href="/agents/grug"`, "dev", "grug", "gpt:41", "140 tokens", "1 session")
}

func TestAgentPageListsItsSessionsWithRoomsAndStatus(t *testing.T) {
	_, h := observe(t)

	code, body := get(t, h, "/agents/dev")

	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, `href="/rooms/obs"`, "obs", `href="/agents/dev/sessions/obs-20260926-064157"`,
		"active", "4 messages", "140")
}

func TestSessionPageShowsTheTranscriptFlaggedWhenItEndedSilently(t *testing.T) {
	_, h := observe(t)

	code, body := get(t, h, "/agents/dev/sessions/obs-20260926-064157")

	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body,
		"@dev deploy it again",
		"go test ./...",
		"FAIL ui [build failed]",
		"the build broke on the host, not here",
		`href="/rooms/obs"`,
		"ended without an answer")
	// Reasoning is folded away: present, but inside a closed <details>.
	if i := strings.Index(body, "the tests will pass this time"); i == -1 {
		t.Error("the page dropped the reasoning entirely")
	} else if j := strings.LastIndex(body[:i], "<details class=\"reasoning\">"); j == -1 || strings.Contains(body[j:i], "</details>") {
		t.Error("reasoning is on the page outside a folded <details>")
	}
	if strings.Contains(body, "<details class=\"reasoning\" open") {
		t.Error("reasoning should start closed")
	}
}

func TestASessionPageIsNotFoundForAnotherAgent(t *testing.T) {
	_, h := observe(t)
	if code, _ := get(t, h, "/agents/grug/sessions/obs-20260926-064157"); code != http.StatusNotFound {
		t.Errorf("status %d, want 404", code)
	}
}

func TestAgentsLinksFromTheRoomList(t *testing.T) {
	_, h := observe(t)
	_, body := get(t, h, "/")
	mustContain(t, body, `href="/agents"`)
}

func TestAgentStoreIsTheReadViewOfAnAgentsDatabase(t *testing.T) {
	// A turso.Store is one, and that is what garage serve hands the UI.
	var _ ui.AgentStore = &turso.Store{}
}
