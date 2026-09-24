package chatroom

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/mail"
)

// RelayMail is one pass of the experimental bridge to a laptop: it posts what
// arrived in mail/to-host, then mails agents' messages in the rooms mail came
// from to mail/to-laptop. Its cursors live in this database.
func (s *Service) RelayMail(ctx context.Context, c *bucket.Caller) error {
	seen, ok, err := s.cursor(ctx, "seen")
	if err != nil {
		return err
	}
	if !ok { // the first pass mails nothing from before it
		if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM messages`).Scan(&seen); err != nil {
			return fmt.Errorf("chatroom: %w", err)
		}
	}

	in, _, err := s.cursor(ctx, "to-host")
	if err != nil {
		return err
	}
	in, err = mail.Receive(ctx, c, mail.ToHost, in, func(seq int64, m mail.Message) error {
		if _, err := s.Post(ctx, m.Room, m.Author, m.Text); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO mail_rooms (room) VALUES (?)`, m.Room); err != nil {
			return fmt.Errorf("chatroom: %w", err)
		}
		return s.setCursor(ctx, "to-host", seq+1)
	})
	if err != nil {
		return err
	}
	if err := s.setCursor(ctx, "to-host", in); err != nil {
		return err
	}

	out, _, err := s.cursor(ctx, "to-laptop")
	if err != nil {
		return err
	}
	msgs, err := s.query(ctx, `SELECT id, room, author, text, created_at FROM messages
		WHERE id > ? AND room IN (SELECT room FROM mail_rooms) ORDER BY id`, seen)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if s.isMember(m.Author) {
			seq, err := mail.Send(ctx, c, mail.ToLaptop, out, mail.Message{Room: m.Room, Author: m.Author, Text: m.Text, Time: m.Time})
			if err != nil {
				return err
			}
			out = seq + 1
			if err := s.setCursor(ctx, "to-laptop", out); err != nil {
				return err
			}
		}
		seen = m.ID
		if err := s.setCursor(ctx, "seen", seen); err != nil {
			return err
		}
	}
	return s.setCursor(ctx, "seen", seen)
}

func (s *Service) isMember(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.members[name]
	return ok
}

func (s *Service) cursor(ctx context.Context, name string) (int64, bool, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT n FROM mail_cursors WHERE name = ?`, name).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("chatroom: %w", err)
	}
	return n, true, nil
}

func (s *Service) setCursor(ctx context.Context, name string, n int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mail_cursors (name, n) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET n = excluded.n`, name, n)
	if err != nil {
		return fmt.Errorf("chatroom: %w", err)
	}
	return nil
}
