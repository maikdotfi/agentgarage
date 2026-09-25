// Package chatroom is where agents and humans talk. Messages live in the
// service's own SQLite database, and a mention (@name) wakes that agent.
package chatroom

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	tursodrv "turso.tech/database/tursogo"
)

// Message is one post in a room. It is never edited.
type Message struct {
	ID     int64     `json:"id"`
	Room   string    `json:"room"`
	Author string    `json:"author"`
	Text   string    `json:"text"`
	Time   time.Time `json:"time"`
}

// Handler is what a joined agent does when it is mentioned.
type Handler func(ctx context.Context, m Message)

// Service is the chatroom: the record of every room and who to wake.
type Service struct {
	db     *sql.DB
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	members map[string]chan Message
	changed chan struct{} // closed and replaced on every post
}

// Open opens (or creates) the chatroom database at path; ":memory:" is fine for tests.
func Open(ctx context.Context, path string) (*Service, error) {
	conn, err := tursodrv.NewConnector(path)
	if err != nil {
		return nil, fmt.Errorf("chatroom: %w", err)
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS messages (
			id         INTEGER PRIMARY KEY,
			room       TEXT NOT NULL,
			author     TEXT NOT NULL,
			text       TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mail_cursors (name TEXT PRIMARY KEY, n INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS mail_rooms (room TEXT PRIMARY KEY)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("chatroom: %w", err)
		}
	}
	sctx, cancel := context.WithCancel(context.Background())
	return &Service{db: db, ctx: sctx, cancel: cancel, members: map[string]chan Message{}, changed: make(chan struct{})}, nil
}

// Close stops waking agents, waits for the handlers running now, and closes the database.
func (s *Service) Close() error {
	s.cancel()
	s.wg.Wait()
	return s.db.Close()
}

// Join makes name an agent that is woken when mentioned. Its handler runs one
// mention at a time, in order.
func (s *Service) Join(name string, h Handler) {
	inbox := make(chan Message, 100)
	s.mu.Lock()
	s.members[name] = inbox
	s.mu.Unlock()
	s.wg.Go(func() {
		for {
			select {
			case m := <-inbox:
				h(s.ctx, m)
			case <-s.ctx.Done():
				return
			}
		}
	})
}

var mention = regexp.MustCompile(`@([A-Za-z][A-Za-z0-9_-]*)`)

// Post appends a message to room and wakes every agent it mentions, except its author.
func (s *Service) Post(ctx context.Context, room, author, text string) (Message, error) {
	m := Message{Room: room, Author: author, Text: text, Time: time.Now().UTC()}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (room, author, text, created_at) VALUES (?, ?, ?, ?)`,
		m.Room, m.Author, m.Text, m.Time.Format(time.RFC3339Nano))
	if err != nil {
		return Message{}, fmt.Errorf("chatroom: %w", err)
	}
	if m.ID, err = res.LastInsertId(); err != nil {
		return Message{}, fmt.Errorf("chatroom: %w", err)
	}

	s.mu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	woken := map[string]bool{}
	for _, match := range mention.FindAllStringSubmatch(text, -1) {
		name := match[1]
		inbox, ok := s.members[name]
		if !ok || name == author || woken[name] {
			continue
		}
		woken[name] = true
		select {
		case inbox <- m:
		default:
			slog.Warn("chatroom: inbox full, mention dropped", "agent", name, "room", room)
		}
	}
	s.mu.Unlock()
	return m, nil
}

// Read is every message in room after the message with ID after, oldest first.
func (s *Service) Read(ctx context.Context, room string, after int64) ([]Message, error) {
	return s.query(ctx, `SELECT id, room, author, text, created_at FROM messages WHERE room = ? AND id > ? ORDER BY id`,
		room, after)
}

// Rooms is the latest message in every room, the most recently active room first.
func (s *Service) Rooms(ctx context.Context) ([]Message, error) {
	return s.query(ctx, `SELECT id, room, author, text, created_at FROM messages
		WHERE id IN (SELECT MAX(id) FROM messages GROUP BY room) ORDER BY id DESC`)
}

func (s *Service) query(ctx context.Context, query string, args ...any) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("chatroom: %w", err)
	}
	defer rows.Close()
	msgs := []Message{}
	for rows.Next() {
		var m Message
		var at string
		if err := rows.Scan(&m.ID, &m.Room, &m.Author, &m.Text, &at); err != nil {
			return nil, fmt.Errorf("chatroom: %w", err)
		}
		m.Time, _ = time.Parse(time.RFC3339Nano, at)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// Wait is Read, except that it blocks until there is something to return or ctx is done.
func (s *Service) Wait(ctx context.Context, room string, after int64) ([]Message, error) {
	for {
		s.mu.Lock()
		changed := s.changed
		s.mu.Unlock()
		msgs, err := s.Read(ctx, room, after)
		if err != nil || len(msgs) > 0 {
			return msgs, err
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return msgs, ctx.Err()
		}
	}
}

// Transcript renders messages the way an agent reads a room.
func Transcript(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "%s: %s\n", m.Author, m.Text)
	}
	return b.String()
}

// Snapshot writes a consistent copy of the whole database to path, which must
// not exist yet. It doesn't stop anyone posting.
func (s *Service) Snapshot(ctx context.Context, path string) error {
	// Turso takes only a literal here and doesn't unescape '' in one.
	if strings.ContainsRune(path, '\'') {
		return fmt.Errorf("chatroom: snapshot path %q has a quote", path)
	}
	_, err := s.db.ExecContext(ctx, "VACUUM INTO '"+path+"'")
	if err != nil {
		return fmt.Errorf("chatroom: %w", err)
	}
	return nil
}
