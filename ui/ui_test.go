package ui_test

import (
	"bufio"
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

func TestRoomPageListensForMessagesAfterTheLastOneShown(t *testing.T) {
	chat, h := garage(t)
	seen := post(t, chat, "a", "mike", "already on the page")

	_, page := get(t, h, "/rooms/a")

	mustContain(t, page, `data-events="/rooms/a/events?after=`+strconv.FormatInt(seen.ID, 10)+`"`, "EventSource")
	if strings.Contains(page, "hx-trigger") {
		t.Error("the room page still polls")
	}
}

// event is one Server-Sent Event: its id and its data lines joined.
type event struct{ id, data string }

// stream opens path on srv as an event stream, with Last-Event-ID if given.
func stream(t *testing.T, srv *httptest.Server, path, lastID string) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return bufio.NewReader(resp.Body)
}

// next reads the next event the way a browser does: CR, LF and CRLF all end
// a line, and lines that aren't a field it knows are dropped.
func next(t *testing.T, r *bufio.Reader) event {
	t.Helper()
	got := make(chan event, 1)
	go func() {
		var ev event
		var data []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			for i, part := range strings.Split(strings.TrimSuffix(line, "\n"), "\r") {
				if i == 0 && part == "" && len(data) > 0 {
					ev.data = strings.Join(data, "\n")
					got <- ev
					return
				}
				if v, ok := strings.CutPrefix(part, "data: "); ok {
					data = append(data, v)
				} else if v, ok := strings.CutPrefix(part, "id: "); ok {
					ev.id = v
				}
			}
		}
	}()
	select {
	case ev := <-got:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
		return event{}
	}
}

func TestNewMessagesArriveAsEventsRenderedLikeThePage(t *testing.T) {
	chat, h := garage(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	seen := post(t, chat, "a", "mike", "already on the page")
	events := stream(t, srv, "/rooms/a/events?after="+strconv.FormatInt(seen.ID, 10), "")

	post(t, chat, "b", "mike", "another room")
	reply := post(t, chat, "a", "dev", "a <b>reply</b>")
	ev := next(t, events)

	if ev.id != strconv.FormatInt(reply.ID, 10) {
		t.Errorf("event id = %q, want the message's %d", ev.id, reply.ID)
	}
	mustContain(t, ev.data, `id="m`+ev.id+`"`, "dev", "a &lt;b&gt;reply&lt;/b&gt;")
	if strings.Contains(ev.data, "already on the page") || strings.Contains(ev.data, "another room") {
		t.Errorf("event carries more than the new message:\n%s", ev.data)
	}
}

func TestAReconnectResumesAfterTheLastEventID(t *testing.T) {
	chat, h := garage(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	first := post(t, chat, "a", "mike", "one")
	post(t, chat, "a", "mike", "two")
	post(t, chat, "a", "mike", "three")

	events := stream(t, srv, "/rooms/a/events?after=0", strconv.FormatInt(first.ID, 10))

	if ev := next(t, events); !strings.Contains(ev.data, "two") {
		t.Errorf("first event after a reconnect = %q, want two", ev.data)
	}
	if ev := next(t, events); !strings.Contains(ev.data, "three") {
		t.Errorf("second event = %q, want three", ev.data)
	}
}

func TestAMultiLineMessageArrivesWhole(t *testing.T) {
	chat, h := garage(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	events := stream(t, srv, "/rooms/a/events", "")

	post(t, chat, "a", "mike", "one\r\ntwo\rthree\nfour")

	mustContain(t, next(t, events).data, "one", "two", "three", "four")
}

func TestAStreamEndsWhenTheBrowserGoesAway(t *testing.T) {
	_, h := garage(t)
	srv := httptest.NewServer(h)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/rooms/a/events", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cancel()

	closed := make(chan struct{})
	go func() { srv.Close(); close(closed) }() // Close waits for every request to finish
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream kept running after the browser left")
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
