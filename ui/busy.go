package ui

import (
	"sync"
	"time"
)

// Busy is one agent mid-turn: the room it is working in, and since when.
// `garage serve` counts busy agents to time a restart; this is the same
// count, saying who and where, so the pages can show it.
type Busy struct {
	mu    sync.Mutex
	room  string
	since time.Time
}

// Start marks a turn in room, from now.
func (b *Busy) Start(room string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.room, b.since = room, time.Now()
}

// End marks the turn over. Nothing is recorded about how it went; the
// session's transcript says that, and the pages read it there.
func (b *Busy) End() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.room, b.since = "", time.Time{}
}

// Working is the room being worked in, and when that turn began. An empty
// room means no turn is running.
func (b *Busy) Working() (string, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.room, b.since
}
