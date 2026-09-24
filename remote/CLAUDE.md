# remote

The garage door remote. **This is the one corner of the garage that runs on a
human's laptop.** Everything else is deployed server side on the host. It's how
a human sees what agents are doing, talks to them, hands them work, and gets
in when something needs fixing.

It ships as `garage remote`, a subcommand of the same binary, so there's one
build and one release. The code lives here and is kept apart from host code.

## What it does

- **Local UI**: a web UI on `localhost`, rendered by the remote itself.
- **Mail** (experimental, chat only for now): reads `mail/to-laptop/` and
  writes `mail/to-host/` through the budgeted bucket client. It polls every
  10s, and only while the remote is running.
- **The door**: STUN, the signed "open the door" request, and the laptop side
  of WireGuard. Once the tunnel is up, the UI talks to the host's HTTP API
  over it, and SSH works too.
- **Keys**: holds the laptop's signing key, the host keys it trusts, and the
  master key's public recipient, all pinned locally. It encrypts secrets
  before writing them to the bucket, and it never holds the master private
  key.

*Built so far*, all in `cmd/garage/remote.go` (no UI yet):

- `garage remote secret NAME` encrypts stdin to the recipient in
  `~/.garage/recipient` and writes it to the bucket.
- `garage remote config` checks a `config/garage.json` exactly as `serve` will
  read it, and only then writes it.
- `garage remote chat -room R` prints what's waiting in `mail/to-laptop/`,
  mails each stdin line to R, and checks again every 10s until stdin ends.
  Its cursors are `~/.garage/mail.json`.

With the door closed, the UI still works for chat through mail, just slower.
With it open, the UI gets live updates from the host over the tunnel.

## Stack

- `html/template`, server-rendered by the remote. Pages are Go handlers.
- [htmx](https://htmx.org), vendored as a single file under `static/`, for
  partial updates. No other JS framework.
- Server-Sent Events for anything live. SSE over WebSockets: it's plain HTTP,
  one direction is enough, and the browser reconnects by itself.
- Plain CSS in one file. No preprocessor, no utility framework.
- Templates and static files are `//go:embed`ed. `go build` is the build.

## Rules

- The remote shares the bucket client, mail format, and signing with the host
  through top-level packages. It never keeps its own copy of the wire format.
- It must keep working when the host is down. Showing what's in the bucket
  and queueing mail are always possible.
- Listen on `127.0.0.1` only.
- No npm, no bundler, no `node_modules`. If something seems to need one, stop
  and ask.
- Handler tests use `net/http/httptest` and assert on what a human would see
  (status, visible text, links), not on template internals.
- Hand-written JS is a last resort, kept inline and small.
- Design direction: dense, legible, tool-like. It's a garage, not a landing
  page.
