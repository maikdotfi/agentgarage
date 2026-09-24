package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
	"github.com/maikdotfi/agentgarage/bucket/mail"
)

func mailPair(t *testing.T) (laptop, host *bucket.Caller, store *bucket.Fake) {
	t.Helper()
	lpub, lpriv, _ := ed25519.GenerateKey(nil)
	hpub, hpriv, _ := ed25519.GenerateKey(nil)
	store = bucket.NewFake()
	limits := map[string]bucket.Limit{"mail": {Every: time.Millisecond, Burst: 100}}
	laptop = bucket.New(store, lpriv, []ed25519.PublicKey{hpub}, limits).Caller("mail")
	host = bucket.New(store, hpriv, []ed25519.PublicKey{lpub}, limits).Caller("mail")
	return laptop, host, store
}

func hostReads(t *testing.T, host *bucket.Caller) []mail.Message {
	t.Helper()
	var got []mail.Message
	mail.Receive(context.Background(), host, mail.ToHost, 0, func(_ int64, m mail.Message) error {
		got = append(got, m)
		return nil
	})
	return got
}

func TestRemoteChatShowsWhatWaitsAndMailsWhatYouType(t *testing.T) {
	ctx := context.Background()
	laptop, host, _ := mailPair(t)
	cursors := filepath.Join(t.TempDir(), "mail.json")
	mail.Send(ctx, host, mail.ToLaptop, 0, mail.Message{Room: "fix", Author: "dev", Text: "done, see the PR"})

	var out bytes.Buffer
	err := remoteChat(ctx, laptop, cursors, "fix", "mike", time.Hour, strings.NewReader("@dev thanks\n\n"), &out)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "#fix dev: done, see the PR") {
		t.Errorf("output does not show the waiting reply:\n%s", out.String())
	}
	got := hostReads(t, host)
	if len(got) != 1 || got[0].Room != "fix" || got[0].Author != "mike" || got[0].Text != "@dev thanks" {
		t.Errorf("host received %+v", got)
	}
}

func TestRemoteChatRemembersWhereItWas(t *testing.T) {
	ctx := context.Background()
	laptop, host, store := mailPair(t)
	cursors := filepath.Join(t.TempDir(), "mail.json")
	mail.Send(ctx, host, mail.ToLaptop, 0, mail.Message{Room: "fix", Author: "dev", Text: "old news"})
	remoteChat(ctx, laptop, cursors, "fix", "mike", time.Hour, strings.NewReader("one\n"), &bytes.Buffer{})

	before := store.Counts()
	var out bytes.Buffer
	remoteChat(ctx, laptop, cursors, "fix", "mike", time.Hour, strings.NewReader("two\n"), &out)

	if strings.Contains(out.String(), "old news") {
		t.Errorf("a second run showed old mail again:\n%s", out.String())
	}
	if a := store.Counts().ClassA - before.ClassA; a != 1 {
		t.Errorf("the second message took %d creates, want 1", a)
	}
	if got := hostReads(t, host); len(got) != 2 || got[1].Text != "two" {
		t.Errorf("host received %+v", got)
	}
}

func TestRemoteConfigRefusesWhatServeCouldNotRun(t *testing.T) {
	laptop, _, store := mailPair(t)
	for _, bad := range []string{`{}`, `{"workspaces": {}}`, `{"workspace": {"a": "b"}}`, `not json`} {
		if err := remoteConfig(context.Background(), laptop, strings.NewReader(bad)); err == nil {
			t.Errorf("config %s was accepted", bad)
		}
	}
	if a := store.Counts().ClassA; a != 0 {
		t.Errorf("%d writes for configs that were refused", a)
	}
}

func TestRemoteConfigIsWhatServeReads(t *testing.T) {
	laptop, host, _ := mailPair(t)
	err := remoteConfig(context.Background(), laptop, strings.NewReader(`{"workspaces": {"agentgarage": "https://github.com/maikdotfi/agentgarage"}}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspaces["agentgarage"] != "https://github.com/maikdotfi/agentgarage" || cfg.Model == "" {
		t.Errorf("config = %+v, want the workspace and a default model", cfg)
	}
}
