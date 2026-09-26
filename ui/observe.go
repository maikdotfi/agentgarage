package ui

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/maikdotfi/agentgarage/agents"
	"github.com/maikdotfi/agentgarage/metaharness/agent"
)

// agentPageData is what /agents/{name} renders: the agent's sessions, each
// joined to its room.
type agentPageData struct {
	Agent    Agent
	Sessions []sessionRow
}

// sessionRow is one session with the room that owns it.
type sessionRow struct {
	ID     string
	Room   string // may be empty: not every session belongs to a room
	Status string
	agent.SessionInfo
	Updated string
}

// agentsPage renders /agents: every agent, its model, and who is mid-turn.
// A store that cannot be read is an error, not a quiet zero: a page whose
// job is truth must not render "0 sessions, idle" over a failed read.
func (s *server) agentsPage(w http.ResponseWriter, r *http.Request) {
	rows := make([]agentRow, 0, len(s.agents))
	for _, a := range s.agents {
		row := agentRow{Agent: a, Rooms: map[string]string{}}
		if a.Store != nil {
			rooms, err := roomSessions(r.Context(), a.Store)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			row.Rooms = rooms
			infos, err := a.Store.ListSessions(r.Context(), 0)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			row.Sessions = infos
			for _, info := range row.Sessions {
				row.Tokens += info.Usage.TotalTokens
			}
		}
		// Mid-turn comes from Busy, the live count serve keeps — not from a
		// session's status, which a crash leaves `active` forever.
		if a.Busy != nil {
			if room, since := a.Busy.Working(); room != "" {
				row.WorkingRoom, row.WorkingSince = room, since
			}
		}
		rows = append(rows, row)
	}
	s.render(w, http.StatusOK, s.pages["agents.html"], "index.html", rows)
}

// agentRow is one agent on /agents. Working state is live, from Busy: a
// session's status is only ever as fresh as the last thing that wrote it, and
// a crash leaves `active` behind — a status the page would trust forever.
type agentRow struct {
	Agent
	Rooms        map[string]string // room -> session id
	Sessions     []agent.SessionInfo
	Tokens       int64     // every session's tokens together
	WorkingRoom  string    // where the current turn is, "" when idle
	WorkingSince time.Time // and since when
}

// agentPage renders /agents/{name}: its sessions, newest first.
func (s *server) agentPage(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agent(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	d := agentPageData{Agent: a}
	if a.Store != nil {
		infos, err := a.Store.ListSessions(r.Context(), 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rooms, err := roomSessions(r.Context(), a.Store)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bySession := map[string]string{}
		for room, id := range rooms {
			bySession[id] = room
		}
		for _, info := range infos {
			d.Sessions = append(d.Sessions, sessionRow{
				ID: info.ID, Room: bySession[info.ID], Status: string(info.Status),
				SessionInfo: info,
			})
		}
	}
	s.render(w, http.StatusOK, s.pages["agent.html"], "index.html", d)
}

// sessionPage renders /agents/{name}/sessions/{id}: the transcript.
func (s *server) sessionPage(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agent(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if a.Store == nil {
		http.NotFound(w, r)
		return
	}
	sess, err := a.Store.Load(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, agent.ErrNotFound) {
			http.NotFound(w, r) // gone, or never was; either way there is no page
			return
		}
		// A read that failed is not a page that is missing.
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rooms, err := roomSessions(r.Context(), a.Store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	room := ""
	for rm, id := range rooms {
		if id == sess.ID {
			room = rm
		}
	}
	s.render(w, http.StatusOK, s.pages["session.html"], "index.html", sessionData{
		Agent: a.Name, Room: room, Session: sess, Turns: turns(sess), NoAnswer: noAnswer(sess),
	})
}

func (s *server) agent(name string) (Agent, bool) {
	for _, a := range s.agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// roomSessions maps each room to the session behind it, as dev keeps that
// join in its own database; agents.RoomSessions is the one copy of it. Every
// agent's store is scanned, not just dev's: the pages don't know which agent
// keeps it.
func roomSessions(ctx context.Context, store AgentStore) (map[string]string, error) {
	return agents.RoomSessions(ctx, store)
}

// roomSession is the session behind one room, or "" when there is none.
func roomSession(ctx context.Context, store AgentStore, room string) string {
	rooms, _ := roomSessions(ctx, store)
	return rooms[room]
}
