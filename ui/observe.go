package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

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
func (s *server) agentsPage(w http.ResponseWriter, r *http.Request) {
	rows := make([]agentRow, 0, len(s.agents))
	for _, a := range s.agents {
		row := agentRow{Agent: a, Rooms: map[string]string{}}
		if a.Store != nil {
			row.Rooms, _ = roomSessions(r.Context(), a.Store)
			row.Sessions, _ = a.Store.ListSessions(r.Context(), allSessions)
			for _, info := range row.Sessions {
				row.Tokens += info.Usage.TotalTokens
				if info.Status == agent.StatusActive {
					row.Working = true
				}
			}
		}
		if a.Busy != nil {
			if room, since := a.Busy.Working(); room != "" {
				row.WorkingRoom, row.WorkingSince = room, since
			}
		}
		rows = append(rows, row)
	}
	s.render(w, http.StatusOK, s.pages["agents.html"], "index.html", rows)
}

// agentRow is one agent on /agents.
type agentRow struct {
	Agent
	Rooms        map[string]string // room -> session id
	Sessions     []agent.SessionInfo
	Tokens       int64     // every session's tokens together
	Working      bool      // a session of this agent is mid-turn now
	WorkingRoom  string    // where, when Busy says so
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
		rooms, _ := roomSessions(r.Context(), a.Store)
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
		http.NotFound(w, r) // gone, or never was; either way there is no page
		return
	}
	rooms, _ := roomSessions(r.Context(), a.Store)
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

// roomsKVPrefix is dev's (agents.RoomsKVPrefix), spelled here so ui does not
// import agents. The prefix is dev's join of room to session.
const roomsKVPrefix = "dev/rooms/"

// allSessions is the limit that means every session: ListSessions(0) returns
// none, so the pages ask for all of them.
const allSessions = 1000000

func roomSessions(ctx context.Context, store AgentStore) (map[string]string, error) {
	entries, err := store.List(ctx, roomsKVPrefix)
	if err != nil {
		return nil, err
	}
	rooms := map[string]string{}
	for _, e := range entries {
		var rec struct{ Task string }
		if err := json.Unmarshal(e.Value, &rec); err != nil {
			continue // one unreadable room doesn't hide the rest
		}
		rooms[strings.TrimPrefix(e.Key, roomsKVPrefix)] = rec.Task
	}
	return rooms, nil
}

// roomSession is the session behind one room, or "" when there is none.
func roomSession(ctx context.Context, store AgentStore, room string) string {
	raw, found, err := store.Get(ctx, roomsKVPrefix+room)
	if err != nil || !found {
		return ""
	}
	var rec struct{ Task string }
	if err := json.Unmarshal(raw, &rec); err != nil {
		return ""
	}
	return rec.Task
}
