package ui_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/ui"
)

func TestAgentsPageShowsWhoIsMidTurnInWhichRoom(t *testing.T) {
	chat, _ := observe(t)
	store := agentStore(t, "dev")
	busy := &ui.Busy{}
	web, err := ui.New(chat, ui.Agent{Name: "dev", Model: "gpt:41", Store: store, Busy: busy})
	if err != nil {
		t.Fatal(err)
	}

	busy.Start("obs")

	code, body := get(t, web, "/agents")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "working") || !strings.Contains(body, `href="/rooms/obs"`) {
		t.Errorf("the busy agent isn't shown working in its room:\n%s", body)
	}

	busy.End()
	_, body = get(t, web, "/agents")
	if strings.Contains(body, "working") {
		t.Errorf("the agent is still shown working after its turn ended:\n%s", body)
	}
}

func TestBusyReportsTheRoomAndSince(t *testing.T) {
	busy := &ui.Busy{}
	if room, since := busy.Working(); room != "" || !since.IsZero() {
		t.Errorf("a fresh Busy = %q, %v, want no turn", room, since)
	}
	busy.Start("obs")
	room, since := busy.Working()
	if room != "obs" || time.Since(since) > time.Second {
		t.Errorf("Working() = %q, %v, want obs and now", room, since)
	}
	busy.End()
	if room, _ := busy.Working(); room != "" {
		t.Errorf("after End(), Working() = %q, want no turn", room)
	}
}

func TestRoomPageLinksToTheSessionBehindIt(t *testing.T) {
	_, h := observe(t)

	code, room := get(t, h, "/rooms/obs")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(room, `href="/agents/dev/sessions/obs-20260926-064157"`) {
		t.Errorf("the room page does not link to the session behind it:\n%s", room)
	}
}
