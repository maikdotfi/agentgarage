package chatroom_test

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/mail"
	"github.com/maikdotfi/agentgarage/chatroom"
)

// bucketPair is a laptop and a host sharing one fake bucket.
func bucketPair(t *testing.T) (laptop, host *bucket.Caller, store *bucket.Fake) {
	t.Helper()
	lpub, lpriv, _ := ed25519.GenerateKey(nil)
	hpub, hpriv, _ := ed25519.GenerateKey(nil)
	store = bucket.NewFake()
	limits := map[string]bucket.Limit{"mail": {Every: time.Millisecond, Burst: 100}}
	laptop = bucket.New(store, lpriv, []ed25519.PublicKey{hpub}, limits).Caller("mail")
	host = bucket.New(store, hpriv, []ed25519.PublicKey{lpub}, limits).Caller("mail")
	return laptop, host, store
}

func relay(t *testing.T, s *chatroom.Service, c *bucket.Caller) {
	t.Helper()
	if err := s.RelayMail(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func laptopReads(t *testing.T, c *bucket.Caller, next int64) []string {
	t.Helper()
	var got []string
	_, err := mail.Receive(context.Background(), c, mail.ToLaptop, next, func(_ int64, m mail.Message) error {
		got = append(got, m.Room+" "+m.Author+": "+m.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMailFromTheLaptopWakesTheAgentAndItsReplyIsMailedBack(t *testing.T) {
	ctx := context.Background()
	laptop, host, _ := bucketPair(t)
	s := open(t, ":memory:")
	s.Join("dev", func(ctx context.Context, m chatroom.Message) {
		s.Post(ctx, m.Room, "dev", "on it")
	})
	mail.Send(ctx, laptop, mail.ToHost, 0, mail.Message{Room: "fix", Author: "mike", Text: "@dev fix it"})

	relay(t, s, host)
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	msgs, _ := s.Wait(waitCtx, "fix", 1)
	if len(msgs) == 0 {
		t.Fatal("dev never replied")
	}
	relay(t, s, host)

	if got := laptopReads(t, laptop, 0); len(got) != 1 || got[0] != "fix dev: on it" {
		t.Errorf("laptop got %q, want only dev's reply", got)
	}
}

func TestOnlyAgentsInRoomsThatGotMailAreMailedOut(t *testing.T) {
	ctx := context.Background()
	laptop, host, _ := bucketPair(t)
	s := open(t, ":memory:")
	s.Join("dev", func(context.Context, chatroom.Message) {})
	post(t, s, "old", "dev", "from before the relay ran")
	relay(t, s, host)

	mail.Send(ctx, laptop, mail.ToHost, 0, mail.Message{Room: "fix", Author: "mike", Text: "hello"})
	relay(t, s, host)
	post(t, s, "fix", "dev", "to the laptop")
	post(t, s, "fix", "alice", "a human over ssh")
	post(t, s, "elsewhere", "dev", "no laptop here")
	relay(t, s, host)

	if got := laptopReads(t, laptop, 0); len(got) != 1 || got[0] != "fix dev: to the laptop" {
		t.Errorf("laptop got %q", got)
	}
}

func TestTheRelayPicksUpWhereItLeftOffAfterARestart(t *testing.T) {
	ctx := context.Background()
	laptop, host, store := bucketPair(t)
	path := filepath.Join(t.TempDir(), "chatroom.db")
	s, err := chatroom.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Join("dev", func(context.Context, chatroom.Message) {})
	mail.Send(ctx, laptop, mail.ToHost, 0, mail.Message{Room: "fix", Author: "mike", Text: "one"})
	relay(t, s, host)
	s.Post(ctx, "fix", "dev", "first reply")
	relay(t, s, host)
	s.Close()

	s = open(t, path)
	s.Join("dev", func(context.Context, chatroom.Message) {})
	mail.Send(ctx, laptop, mail.ToHost, 1, mail.Message{Room: "fix", Author: "mike", Text: "two"})
	before := store.Counts()
	relay(t, s, host)
	s.Post(ctx, "fix", "dev", "second reply")
	relay(t, s, host)
	after := store.Counts()

	msgs, _ := s.Read(ctx, "fix", 0)
	if got := texts(msgs); len(got) != 4 || got[2] != "mike: two" {
		t.Errorf("room = %q, want each human message posted once", got)
	}
	if got := laptopReads(t, laptop, 0); len(got) != 2 || got[1] != "fix dev: second reply" {
		t.Errorf("laptop got %q, want each reply mailed once", got)
	}
	// Two passes: GET two, GET the gap, GET the gap again, and one create for the reply.
	if a, b := after.ClassA-before.ClassA, after.ClassB-before.ClassB; a != 1 || b != 3 {
		t.Errorf("ops after restart = %d class A, %d class B; want 1 create and 3 GETs", a, b)
	}
}
