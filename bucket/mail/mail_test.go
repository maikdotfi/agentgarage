package mail_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/mail"
)

var fast = bucket.Limit{Every: time.Millisecond, Burst: 100}

// pair is a laptop and a host sharing one bucket, each trusting the other.
func pair(t *testing.T) (laptop, host *bucket.Caller, store *bucket.Fake) {
	t.Helper()
	lpub, lpriv, _ := ed25519.GenerateKey(nil)
	hpub, hpriv, _ := ed25519.GenerateKey(nil)
	store = bucket.NewFake()
	limits := map[string]bucket.Limit{"mail": fast}
	laptop = bucket.New(store, lpriv, []ed25519.PublicKey{hpub}, limits).Caller("mail")
	host = bucket.New(store, hpriv, []ed25519.PublicKey{lpub}, limits).Caller("mail")
	return laptop, host, store
}

func receive(t *testing.T, c *bucket.Caller, box mail.Box, next int64) ([]mail.Message, int64) {
	t.Helper()
	var got []mail.Message
	next, err := mail.Receive(context.Background(), c, box, next, func(_ int64, m mail.Message) error {
		got = append(got, m)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got, next
}

func TestMessagesArriveInTheOrderSent(t *testing.T) {
	ctx := context.Background()
	laptop, host, _ := pair(t)

	next := int64(0)
	for _, text := range []string{"@dev one", "@dev two"} {
		seq, err := mail.Send(ctx, laptop, mail.ToHost, next, mail.Message{Room: "fix", Author: "mike", Text: text})
		if err != nil {
			t.Fatal(err)
		}
		next = seq + 1
	}

	got, next := receive(t, host, mail.ToHost, 0)
	if len(got) != 2 || got[0].Text != "@dev one" || got[1].Text != "@dev two" || got[0].Room != "fix" {
		t.Fatalf("received %+v", got)
	}
	if next != 2 {
		t.Errorf("next = %d, want 2", next)
	}
	if again, _ := receive(t, host, mail.ToHost, next); len(again) != 0 {
		t.Errorf("reading on from next received %+v again", again)
	}
}

func TestAnIdleReadIsOneGet(t *testing.T) {
	_, host, store := pair(t)

	receive(t, host, mail.ToHost, 0)

	if c := store.Counts(); c.ClassA != 0 || c.ClassB != 1 {
		t.Errorf("counts = %+v, want one GET and nothing else", c)
	}
}

func TestASenderBehindSkipsTakenNumbers(t *testing.T) {
	ctx := context.Background()
	laptop, host, _ := pair(t)
	mail.Send(ctx, laptop, mail.ToHost, 0, mail.Message{Room: "a", Author: "mike", Text: "first"})

	seq, err := mail.Send(ctx, laptop, mail.ToHost, 0, mail.Message{Room: "a", Author: "mike", Text: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Errorf("seq = %d, want 1", seq)
	}
	if got, _ := receive(t, host, mail.ToHost, 0); len(got) != 2 || got[1].Text != "second" {
		t.Errorf("received %+v", got)
	}
}

func TestForgedAndMalformedMailIsSkipped(t *testing.T) {
	ctx := context.Background()
	laptop, host, store := pair(t)
	_, strangerKey, _ := ed25519.GenerateKey(nil)
	stranger := bucket.New(store, strangerKey, nil, map[string]bucket.Limit{"mail": fast}).Caller("mail")

	mail.Send(ctx, stranger, mail.ToHost, 0, mail.Message{Room: "a", Author: "mike", Text: "rm -rf"})
	laptop.Create(ctx, "mail/to-host/1", []byte("not json"))
	mail.Send(ctx, laptop, mail.ToHost, 0, mail.Message{Room: "a", Author: "mike", Text: "real"})

	got, next := receive(t, host, mail.ToHost, 0)
	if len(got) != 1 || got[0].Text != "real" {
		t.Errorf("received %+v, want only the real message", got)
	}
	if next != 3 {
		t.Errorf("next = %d, want 3: skipped mail is consumed", next)
	}
}

func TestEachBoxIsItsOwnSequence(t *testing.T) {
	ctx := context.Background()
	laptop, host, _ := pair(t)
	mail.Send(ctx, laptop, mail.ToHost, 0, mail.Message{Room: "a", Author: "mike", Text: "up"})

	seq, _ := mail.Send(ctx, host, mail.ToLaptop, 0, mail.Message{Room: "a", Author: "dev", Text: "down"})
	if seq != 0 {
		t.Errorf("first to-laptop seq = %d, want 0", seq)
	}
	if got, _ := receive(t, laptop, mail.ToLaptop, 0); len(got) != 1 || got[0].Text != "down" {
		t.Errorf("laptop received %+v", got)
	}
}
