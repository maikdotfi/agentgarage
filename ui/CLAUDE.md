# ui

The chat UI, served by `garage serve` on `0.0.0.0:8080` over plain HTTP. No
HTTPS and no login: the host is on a private network, and `garage setup` opens
8080 only to the range it allows SSH from. It's how a human sees what agents
are doing, talks to them, and hands them work.

It talks to the chatroom in the same process: no mail, no bucket, no remote.

## How it works today

- `ui.New(chat)` is the whole UI as one `http.Handler`; `garage serve` serves
  it on `-http` (default `0.0.0.0:8080`).
- `/` lists rooms by their latest message, newest first, with a form that
  opens any room by name. `/rooms/{name}` is the room: messages oldest first,
  and a form to post. A room exists once someone posts in it.
- Humans post as the name in the form, remembered in the `garage-as` cookie.
  There is no login, so the name is only a label, as with `garage chat -as`.
  The form is `hx-boost`ed: it posts, follows the redirect and swaps the page,
  so it works without JS too. Enter sends, Shift+Enter is a new line.
- The poll is the last `<li>` of the list: it GETs
  `/rooms/{name}/messages?after=N` every 2s and is replaced by the new
  messages plus a new poll. Nothing new is a 204, which htmx leaves alone.
- The log stays scrolled to the newest message by CSS (`column-reverse`), not
  JS.

## Stack

- `html/template`, server-rendered. Pages are Go handlers.
- [htmx](https://htmx.org), vendored as a single file under `static/`, for
  partial updates. No other JS framework.
- Live updates are a plain htmx poll at first. Server-Sent Events replace it
  in Phase 4: SSE over WebSockets, since it's plain HTTP, one direction is
  enough, and the browser reconnects by itself.
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
  message.html   a partial: {{define "message"}}, one per file
  messages.html  a partial: messages plus the poll, also the poll's response
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
- A partial is shared by full pages and htmx fragment responses, so a
  message renders the same whether the page loaded it or a poll fetched it.
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
