// Package mail is chat between a laptop and the host through the bucket.
//
// Experimental: it is as small as it can be until agents run on their own and
// we know what they need to say. One signed chat message is one create-only
// object at <box><seq>, and a reader GETs the next number until it is missing.
// There is no cursor object and no LIST; each side keeps its own numbers.
package mail

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
)

// Box is one direction of mail, as a key prefix.
type Box string

const (
	ToHost   Box = "mail/to-host/"
	ToLaptop Box = "mail/to-laptop/"
)

// Message is one chat message for a room.
type Message struct {
	Room   string    `json:"room"`
	Author string    `json:"author"`
	Text   string    `json:"text"`
	Time   time.Time `json:"time"`
}

func (b Box) key(seq int64) string { return string(b) + strconv.FormatInt(seq, 10) }

// Send writes m as the first free number at or after next, and returns the
// number it took. A zero Time is set to now.
func Send(ctx context.Context, c *bucket.Caller, box Box, next int64, m Message) (int64, error) {
	if m.Time.IsZero() {
		m.Time = time.Now().UTC()
	}
	body, err := json.Marshal(m)
	if err != nil {
		return 0, err
	}
	for seq := next; ; seq++ {
		_, err := c.Create(ctx, box.key(seq), body)
		if !errors.Is(err, bucket.ErrExists) {
			return seq, err
		}
	}
}

// Receive reads messages from number next on, calling f with each, until one
// is missing; it returns the number to read next. Forged or malformed mail is
// skipped. An error from f stops it before that message is consumed.
func Receive(ctx context.Context, c *bucket.Caller, box Box, next int64, f func(seq int64, m Message) error) (int64, error) {
	for ; ; next++ {
		obj, err := c.Get(ctx, box.key(next))
		switch {
		case errors.Is(err, bucket.ErrNotFound):
			return next, nil
		case errors.Is(err, bucket.ErrBadSignature):
			slog.Warn("mail: dropped forged mail", "key", box.key(next))
			continue
		case err != nil:
			return next, err
		}
		var m Message
		if err := json.Unmarshal(obj.Body, &m); err != nil || m.Room == "" || m.Text == "" {
			slog.Warn("mail: dropped malformed mail", "key", box.key(next))
			continue
		}
		if err := f(next, m); err != nil {
			return next, err
		}
	}
}
