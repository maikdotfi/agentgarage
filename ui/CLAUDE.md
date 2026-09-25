# ui

The chat UI, served by `garage serve` on `0.0.0.0:8080` over plain HTTP. No
HTTPS and no login: the host is on a private network, and `garage setup` opens
8080 only to the range it allows SSH from. It's how a human sees what agents
are doing, talks to them, and hands them work.

It talks to the chatroom in the same process: no mail, no bucket, no remote.

*Not built yet* (`.plans/ROADMAP.md`, step 11).

## Stack

- `html/template`, server-rendered. Pages are Go handlers.
- [htmx](https://htmx.org), vendored as a single file under `static/`, for
  partial updates. No other JS framework.
- Server-Sent Events for anything live. SSE over WebSockets: it's plain HTTP,
  one direction is enough, and the browser reconnects by itself.
- Plain CSS in one file. No preprocessor, no utility framework.
- Templates and static files are `//go:embed`ed. `go build` is the build.

## Rules

- No npm, no bundler, no `node_modules`. If something seems to need one, stop
  and ask.
- Handler tests use `net/http/httptest` and assert on what a human would see
  (status, visible text, links), not on template internals.
- Hand-written JS is a last resort, kept inline and small.
- Design direction: dense, legible, tool-like. It's a garage, not a landing
  page.
