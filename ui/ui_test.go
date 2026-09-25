package ui_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/ui"
)

// garage is the UI on an in-memory chatroom.
func garage(t *testing.T) (*chatroom.Service, http.Handler) {
	t.Helper()
	chat, err := chatroom.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Close() })
	h, err := ui.New(chat)
	if err != nil {
		t.Fatal(err)
	}
	return chat, h
}

func post(t *testing.T, chat *chatroom.Service, room, author, text string) chatroom.Message {
	t.Helper()
	m, err := chat.Post(context.Background(), room, author, text)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// get is what a browser shows for path: the status and the body.
func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(body)
}

func submit(h http.Handler, path string, form url.Values, cookies ...*http.Cookie) *http.Response {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result()
}

func mustContain(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("page does not show %q:\n%s", s, body)
		}
	}
}

func TestRoomListLinksEveryRoomWithItsLatestMessage(t *testing.T) {
	chat, h := garage(t)
	post(t, chat, "fix-ci", "mike", "@dev the build is red")
	post(t, chat, "garage", "mike", "hello")
	post(t, chat, "fix-ci", "dev", "on it")

	code, body := get(t, h, "/")

	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, `href="/rooms/fix-ci"`, `href="/rooms/garage"`, "on it", "hello")
	if strings.Contains(body, "the build is red") {
		t.Error("room list shows an older message, want only the latest")
	}
	if strings.Index(body, "fix-ci") > strings.Index(body, `href="/rooms/garage"`) {
		t.Error("want the most recently active room first")
	}
}

func TestRoomListOffersToOpenANewRoom(t *testing.T) {
	_, h := garage(t)
	_, body := get(t, h, "/")
	mustContain(t, body, `action="/rooms"`, `name="name"`)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/rooms?name=new+room", nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/rooms/new%20room" {
		t.Errorf("status %d, location %q; want a redirect to the room", w.Code, w.Header().Get("Location"))
	}
}

func TestRoomPageShowsItsMessagesInOrderAndAFormToPost(t *testing.T) {
	chat, h := garage(t)
	post(t, chat, "fix-ci", "mike", "@dev the build is red")
	post(t, chat, "elsewhere", "mike", "not here")
	post(t, chat, "fix-ci", "dev", "on it")

	code, body := get(t, h, "/rooms/fix-ci")

	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "fix-ci", "mike", "@dev the build is red", "dev", "on it",
		`action="/rooms/fix-ci/messages"`, `name="text"`, `name="as"`, `src="/static/htmx.min.js"`)
	if strings.Contains(body, "not here") {
		t.Error("room page shows another room's message")
	}
	if strings.Index(body, "the build is red") > strings.Index(body, "on it") {
		t.Error("want messages oldest first")
	}
}

func TestMessagesShowAsTextNotHTML(t *testing.T) {
	chat, h := garage(t)
	post(t, chat, "a", "mike", "<script>alert(1)</script>")

	_, body := get(t, h, "/rooms/a")

	if strings.Contains(body, "<script>alert(1)") {
		t.Error("message text is rendered as HTML")
	}
	mustContain(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestPostingAsAHumanWakesTheMentionedAgentAndRemembersTheName(t *testing.T) {
	chat, h := garage(t)
	woken := make(chan chatroom.Message, 1)
	chat.Join("dev", func(_ context.Context, m chatroom.Message) { woken <- m })

	resp := submit(h, "/rooms/fix-ci/messages", url.Values{"as": {"mike"}, "text": {"@dev fix the build"}})

	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/rooms/fix-ci" {
		t.Fatalf("status %d, location %q; want back to the room", resp.StatusCode, resp.Header.Get("Location"))
	}
	select {
	case m := <-woken:
		if m.Author != "mike" || m.Text != "@dev fix the build" {
			t.Errorf("dev woken with %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dev was never woken")
	}
	var as *http.Cookie
	for _, c := range resp.Cookies() {
		as = c
	}
	if as == nil {
		t.Fatal("no cookie remembers who posted")
	}
	r := httptest.NewRequest(http.MethodGet, "/rooms/fix-ci", nil)
	r.AddCookie(as)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	mustContain(t, w.Body.String(), `name="as" value="mike"`, "@dev fix the build")
}

func TestPostingWithoutANameOrTextIsRefused(t *testing.T) {
	chat, h := garage(t)
	for _, form := range []url.Values{{"as": {"mike"}}, {"text": {"hi"}}, {"as": {" "}, "text": {"hi"}}} {
		if resp := submit(h, "/rooms/a/messages", form); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", form, resp.StatusCode)
		}
	}
	if msgs, _ := chat.Read(context.Background(), "a", 0); len(msgs) != 0 {
		t.Errorf("posted %d messages", len(msgs))
	}
}

func TestPollingShowsOnlyNewMessages(t *testing.T) {
	chat, h := garage(t)
	seen := post(t, chat, "a", "mike", "already on the page")
	_, page := get(t, h, "/rooms/a")
	mustContain(t, page, `/rooms/a/messages?after=`+strconv.FormatInt(seen.ID, 10))

	poll := "/rooms/a/messages?after=" + strconv.FormatInt(seen.ID, 10)
	if code, _ := get(t, h, poll); code != http.StatusNoContent {
		t.Errorf("nothing new: status %d, want 204", code)
	}

	next := post(t, chat, "a", "dev", "a reply")
	code, body := get(t, h, poll)

	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContain(t, body, "a reply", "dev", `/rooms/a/messages?after=`+strconv.FormatInt(next.ID, 10))
	if strings.Contains(body, "already on the page") || strings.Contains(body, "<html") {
		t.Errorf("poll returns more than the new messages:\n%s", body)
	}
}

func TestStaticFilesAreServed(t *testing.T) {
	_, h := garage(t)
	for _, path := range []string{"/static/htmx.min.js", "/static/garage.css"} {
		if code, body := get(t, h, path); code != http.StatusOK || body == "" {
			t.Errorf("%s: status %d", path, code)
		}
	}
}
