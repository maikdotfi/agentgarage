// Package ui is the chat UI that garage serve serves over plain HTTP: a room
// list, a room page and a form to post, rendered on the server, with htmx
// polling for new messages. It uses the chatroom in process.
package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/maikdotfi/agentgarage/chatroom"
)

var (
	//go:embed templates
	templates embed.FS
	//go:embed static
	static embed.FS
)

// pages are the files that define "main"; every other template is a partial.
var pages = []string{"rooms.html", "room.html"}

// asCookie remembers who a browser posts as. There is no login.
const asCookie = "garage-as"

type server struct {
	chat  *chatroom.Service
	pages map[string]*template.Template
}

// New is the UI for chat. It parses every template now, so a broken one is an
// error here and never in a request.
func New(chat *chatroom.Service) (http.Handler, error) {
	s := &server{chat: chat, pages: map[string]*template.Template{}}
	shell, err := template.New("index.html").Funcs(funcs).ParseFS(templates, "templates/index.html")
	if err != nil {
		return nil, fmt.Errorf("ui: %w", err)
	}
	files, _ := fs.Glob(templates, "templates/*.html")
	for _, f := range files {
		name := path.Base(f)
		if name != "index.html" && !slices.Contains(pages, name) {
			if _, err := shell.ParseFS(templates, f); err != nil {
				return nil, fmt.Errorf("ui: %w", err)
			}
		}
	}
	for _, p := range pages {
		t, err := shell.Clone()
		if err == nil {
			t, err = t.ParseFS(templates, "templates/"+p)
		}
		if err != nil {
			return nil, fmt.Errorf("ui: %w", err)
		}
		s.pages[p] = t
	}

	mux := http.NewServeMux()
	staticFS, _ := fs.Sub(static, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))
	mux.HandleFunc("GET /{$}", s.rooms)
	mux.HandleFunc("GET /rooms", s.open)
	mux.HandleFunc("GET /rooms/{room}", s.room)
	mux.HandleFunc("GET /rooms/{room}/messages", s.poll)
	mux.HandleFunc("POST /rooms/{room}/messages", s.post)
	return mux, nil
}

var funcs = template.FuncMap{
	"roomURL": roomURL,
	// when is a message's time as a glance: the clock today, the date before.
	"when": func(t time.Time) string {
		t = t.Local()
		if t.Format(time.DateOnly) == time.Now().Format(time.DateOnly) {
			return t.Format("15:04")
		}
		return t.Format("Jan 2 15:04")
	},
	"stamp": func(t time.Time) string { return t.Local().Format(time.RFC3339) },
}

func roomURL(room string) string { return "/rooms/" + url.PathEscape(room) }

// roomData is what the room page and its poll render.
type roomData struct {
	Room     string
	As       string
	Messages []chatroom.Message
	After    int64 // the last message shown
}

func (s *server) rooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.chat.Rooms(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, s.pages["rooms.html"], "index.html", rooms)
}

// open is the room list's "open a room" form: any name, new or not.
func (s *server) open(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, roomURL(name), http.StatusSeeOther)
}

func (s *server) room(w http.ResponseWriter, r *http.Request) {
	d, err := s.read(r, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c, err := r.Cookie(asCookie); err == nil {
		d.As, _ = url.QueryUnescape(c.Value)
	}
	s.render(w, http.StatusOK, s.pages["room.html"], "index.html", d)
}

// poll is the messages after ?after=, as a fragment for htmx to append, or
// 204 (which htmx leaves alone) when there are none.
func (s *server) poll(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
	d, err := s.read(r, after)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(d.Messages) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.render(w, http.StatusOK, s.pages["room.html"], "messages", d)
}

func (s *server) read(r *http.Request, after int64) (roomData, error) {
	d := roomData{Room: r.PathValue("room"), After: after}
	msgs, err := s.chat.Read(r.Context(), d.Room, after)
	if err != nil {
		return d, err
	}
	d.Messages = msgs
	if len(msgs) > 0 {
		d.After = msgs[len(msgs)-1].ID
	}
	return d, nil
}

// post adds a human's message, which wakes any agent it mentions, and goes
// back to the room.
func (s *server) post(w http.ResponseWriter, r *http.Request) {
	room := r.PathValue("room")
	as, text := strings.TrimSpace(r.FormValue("as")), strings.TrimSpace(r.FormValue("text"))
	if as == "" || text == "" {
		http.Error(w, "a name and a message are required", http.StatusBadRequest)
		return
	}
	if _, err := s.chat.Post(r.Context(), room, as, text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: asCookie, Value: url.QueryEscape(as), Path: "/", MaxAge: 365 * 24 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, roomURL(room), http.StatusSeeOther)
}

// render executes into a buffer first, so a failed template is a clean 500.
func (s *server) render(w http.ResponseWriter, code int, t *template.Template, name string, data any) {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		slog.Error("ui: render", "template", name, "err", err)
		http.Error(w, "the page failed to render", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	b.WriteTo(w)
}
