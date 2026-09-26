package ui_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/maikdotfi/agentgarage/metaharness/agent"
	"github.com/maikdotfi/agentgarage/metaharness/agentdb"
	"github.com/maikdotfi/agentgarage/ui"
)

// failingStore is an agent's database that cannot be read: every call errors.
// The pages must say so, not render a healthy-looking nothing.
type failingStore struct{ ui.AgentStore }

func (failingStore) ListSessions(context.Context, int) ([]agent.SessionInfo, error) {
	return nil, errStore
}

func (failingStore) Load(context.Context, string) (*agent.Session, error) {
	return nil, errStore
}

func (failingStore) List(context.Context, string) ([]agentdb.Entry, error) {
	return nil, errStore
}

var errStore = &storeError{}

type storeError struct{}

func (*storeError) Error() string { return "the database is locked" }

// TestAgentsPagesDoNotSwallowAStoreThatCannotBeRead: observability that shows
// "0 sessions, idle" when the read failed is the lie these pages exist to kill.
func TestAgentsPagesDoNotSwallowAStoreThatCannotBeRead(t *testing.T) {
	_, h := garageWith(t, ui.Agent{Name: "dev", Model: "x", Store: failingStore{}})

	for _, path := range []string{"/agents", "/agents/dev", "/agents/dev/sessions/any"} {
		code, body := get(t, h, path)
		if code != http.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500 — a failed read must not render fiction:\n%s", path, code, body)
		}
		if !strings.Contains(body, "locked") {
			t.Errorf("%s: the error is not on the page:\n%s", path, body)
		}
	}
}
