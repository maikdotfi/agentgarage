package agents

import (
	"context"
	"encoding/json"
	"strings"
)

// RoomsKVPrefix is where dev keeps each room's task, under key
// <prefix><room>: the room joined to the session behind it.
const RoomsKVPrefix = "dev/rooms/"

// RoomSessions is every room that has a task, joined to that task's session
// id. The observability pages join a room to what the agent behind it did;
// this is that join, from the record dev keeps.
func RoomSessions(ctx context.Context, store Store) (map[string]string, error) {
	entries, err := store.List(ctx, RoomsKVPrefix)
	if err != nil {
		return nil, err
	}
	rooms := map[string]string{}
	for _, e := range entries {
		var rec taskRecord
		if err := json.Unmarshal(e.Value, &rec); err != nil {
			continue // one unreadable room doesn't hide the rest
		}
		rooms[strings.TrimPrefix(e.Key, RoomsKVPrefix)] = rec.Task
	}
	return rooms, nil
}