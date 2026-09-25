package chatroom_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/chatroom"
)

func open(t *testing.T, path string) *chatroom.Service {
	t.Helper()
	s, err := chatroom.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func post(t *testing.T, s *chatroom.Service, room, author, text string) chatroom.Message {
	t.Helper()
	m, err := s.Post(context.Background(), room, author, text)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func texts(msgs []chatroom.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Author+": "+m.Text)
	}
	return out
}

func TestRoomsKeepTheirOwnMessagesInOrder(t *testing.T) {
	s := open(t, ":memory:")
	post(t, s, "a", "mike", "one")
	post(t, s, "b", "mike", "elsewhere")
	post(t, s, "a", "dev", "two")

	msgs, err := s.Read(context.Background(), "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(texts(msgs), "|"); got != "mike: one|dev: two" {
		t.Errorf("room a = %q", got)
	}
	after, _ := s.Read(context.Background(), "a", msgs[0].ID)
	if got := strings.Join(texts(after), "|"); got != "dev: two" {
		t.Errorf("room a after the first = %q", got)
	}
}

func TestRoomsListsEachRoomByItsLatestMessageNewestFirst(t *testing.T) {
	s := open(t, ":memory:")
	post(t, s, "a", "mike", "one")
	post(t, s, "b", "mike", "elsewhere")
	post(t, s, "a", "dev", "two")

	rooms, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range rooms {
		got = append(got, m.Room+" "+m.Author+": "+m.Text)
	}
	if strings.Join(got, "|") != "a dev: two|b mike: elsewhere" {
		t.Errorf("rooms = %q", got)
	}
}

func TestMessagesOutliveTheProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chatroom.db")
	s, err := chatroom.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	post(t, s, "a", "mike", "remember me")
	s.Close()

	msgs, _ := open(t, path).Read(context.Background(), "a", 0)
	if got := strings.Join(texts(msgs), "|"); got != "mike: remember me" {
		t.Errorf("after reopen = %q", got)
	}
}

func TestMentionWakesOnlyThatAgent(t *testing.T) {
	s := open(t, ":memory:")
	woken := make(chan string, 10)
	for _, name := range []string{"dev", "grug"} {
		s.Join(name, func(_ context.Context, m chatroom.Message) {
			woken <- name + " <- " + m.Room + ": " + m.Text
		})
	}

	post(t, s, "fix", "mike", "no mention here")
	post(t, s, "fix", "mike", "@dev do the thing")

	select {
	case got := <-woken:
		if got != "dev <- fix: @dev do the thing" {
			t.Errorf("woken = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("dev was not woken")
	}
	select {
	case got := <-woken:
		t.Errorf("unexpected wake: %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAgentMentioningItselfIsNotWoken(t *testing.T) {
	s := open(t, ":memory:")
	woken := make(chan struct{}, 1)
	s.Join("dev", func(context.Context, chatroom.Message) { woken <- struct{}{} })

	post(t, s, "fix", "dev", "note to self, @dev")
	select {
	case <-woken:
		t.Error("dev woke itself")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAnAgentHandlesOneMentionAtATime(t *testing.T) {
	s := open(t, ":memory:")
	release := make(chan struct{})
	started := make(chan string, 2)
	s.Join("dev", func(_ context.Context, m chatroom.Message) {
		started <- m.Text
		<-release
	})

	post(t, s, "a", "mike", "@dev first")
	post(t, s, "b", "mike", "@dev second")
	if got := <-started; got != "@dev first" {
		t.Fatalf("first handled = %q", got)
	}
	select {
	case got := <-started:
		t.Fatalf("%q started while the first was still running", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if got := <-started; got != "@dev second" {
		t.Errorf("second handled = %q", got)
	}
}

func TestWaitReturnsOnceSomethingNewIsPosted(t *testing.T) {
	s := open(t, ":memory:")
	first := post(t, s, "a", "mike", "hello")

	go func() {
		time.Sleep(20 * time.Millisecond)
		post(t, s, "b", "mike", "other room")
		post(t, s, "a", "dev", "hi")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msgs, err := s.Wait(ctx, "a", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(texts(msgs), "|"); got != "dev: hi" {
		t.Errorf("wait = %q", got)
	}
}

func TestHTTPPostAndRead(t *testing.T) {
	s := open(t, ":memory:")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/rooms/fix/messages", url.Values{"author": {"mike"}, "text": {"@dev hi"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post status = %d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/rooms/fix/messages?after=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var msgs []chatroom.Message
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(texts(msgs), "|"); got != "mike: @dev hi" {
		t.Errorf("read = %q", got)
	}
}

func TestHTTPReadCanWaitForNews(t *testing.T) {
	s := open(t, ":memory:")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	go func() {
		time.Sleep(20 * time.Millisecond)
		post(t, s, "fix", "dev", "done")
	}()
	resp, err := http.Get(srv.URL + "/rooms/fix/messages?after=0&wait=5s")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var msgs []chatroom.Message
	json.NewDecoder(resp.Body).Decode(&msgs)
	if got := strings.Join(texts(msgs), "|"); got != "dev: done" {
		t.Errorf("waited read = %q", got)
	}
}

func TestASnapshotIsAChatroomOfItsOwn(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "chatroom.db"))
	post(t, s, "fix", "mike", "keep this")

	path := filepath.Join(t.TempDir(), "copy.db")
	if err := s.Snapshot(ctx, path); err != nil {
		t.Fatal(err)
	}

	msgs, err := open(t, path).Read(ctx, "fix", 0)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "keep this" {
		t.Errorf("snapshot has %+v, %v", msgs, err)
	}
}

func TestASnapshotPathWithAQuoteIsRefused(t *testing.T) {
	s := open(t, ":memory:")
	path := filepath.Join(t.TempDir(), "it's.db")
	if err := s.Snapshot(context.Background(), path); err == nil {
		t.Error("a quoted path was accepted")
	}
}
