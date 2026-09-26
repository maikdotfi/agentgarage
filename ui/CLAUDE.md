# ui

The chat UI, served by `garage serve` on `0.0.0.0:8080` over plain HTTP. No
HTTPS and no login: the host is on a private network, and `garage setup` opens
8080 only to the range it allows SSH from. It's how a human sees what agents
are doing, talks to them, and hands them work.

It talks to the chatroom in the same process: no mail, no bucket, no remote.

## How it works today

- `ui.New(chat, agents...)` is the whole UI as one `http.Handler`; `garage
  serve` serves it on `-http` (default `0.0.0.0:8080`). Each `ui.Agent` is a
  read-only view of one agent's database (a `turso.Store`, handed over by
  serve), which the agents pages observe: `/agents` lists the agents, their
  model, who is mid-turn where (from `ui.Busy`, the same count serve times
  restarts against), and each room joined to the session behind it;
  `/agents/{name}` lists its sessions; `/agents/{name}/sessions/{id}` is the
  transcript, with reasoning folded into `<details>`, tool calls and results
  shown, failed results marked, and a flag when the session's last assistant
  message had no text. The pages never write: no session, message or kv.
- `/` lists rooms by their latest message, newest first, with a form that
  opens any room by name. `/rooms/{name}` is the room: messages oldest first,
  a link to the session behind it, and a form to post. A room exists once
  someone posts in it.
- Humans post as the name in the form, remembered in the `garage-as` cookie.
  There is no login, so the name is only a label, as with `garage chat -as`.
  The form is `hx-boost`ed: it posts, follows the redirect and swaps the page,
  so it works without JS too. Enter sends, Shift+Enter is a new line.
- New messages stream in over Server-Sent Events from
  `/rooms/{name}/events?after=N` (N is the last message the page shows). Each
  event is the `message` partial, with the message ID as the event ID, so a
  reconnecting browser sends `Last-Event-ID` and resumes there. The stream
  waits on `chatroom.Wait` and ends when the browser leaves or serve stops.
- The client is a few lines of inline JS in `room.html`: an `EventSource`
  that appends each event unless that message is already on the page, and
  closes the previous one when a boosted post swaps the page in again.
- The log stays scrolled to the newest message by CSS (`column-reverse`), not
  JS.

## Stack

- `html/template`, server-rendered. Pages are Go handlers.
- [htmx](https://htmx.org), vendored as a single file under `static/`, for
  partial updates. No other JS framework.
- Live updates are Server-Sent Events: SSE over WebSockets, since it's plain
  HTTP, one direction is enough, and the browser reconnects by itself. A
  bare `EventSource` over the htmx SSE extension: the extension reconnects
  with a fresh `EventSource` that drops `Last-Event-ID`, and it would be a
  second vendored file.
- Plain CSS in one file. No preprocessor, no utility framework.
- Templates and static files are `//go:embed`ed. `go build` is the build.

## Pages and templates

A multipage app: every screen is a real URL the server renders in full, and
navigation is plain links and forms. No client-side routing.

```
templates/
  index.html     the one page shell: head, CSS, htmx, nav, {{block "main" .}}
  rooms.html     a page: defines "main" for /
  room.html      a page: defines "main" for /rooms/{name}
  agents.html    a page: defines "main" for /agents
  agent.html     a page: defines "main" for /agents/{name}
  session.html   a page: defines "main" for /agents/{name}/sessions/{id}
  message.html   a partial: {{define "message"}}, one per file; also every
                 event on a room's stream
static/
  htmx.min.js    vendored, see STACK.md
  garage.css     the one stylesheet
```

A new page goes in `pages` in `ui.go`; every other file in `templates/` is a
partial.

- `index.html` is the only HTML document. Every page is that shell with its
  own `main`, so each page is a small file scoped to one screen.
- Each page gets its own template set: the shell and the partials, cloned,
  plus that page's file. Pages never see each other's blocks.
- A partial is shared by full pages and fragment responses, so a message
  renders the same whether the page loaded it or the stream sent it.
- All templates are parsed once at startup; a broken one fails `serve` and a
  test, never a request.

## Rules

- No npm, no bundler, no `node_modules`. If something seems to need one, stop
  and ask.
- Handler tests use `net/http/httptest` and assert on what a human would see
  (status, visible text, links), not on template internals.
- Hand-written JS is a last resort, kept inline and small.
- Design direction: dense, legible, tool-like. It's a garage, not a landing
  page.
