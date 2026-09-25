# remote

The garage remote. **This is the one corner of the garage that runs on a
human's laptop.** Everything else is deployed server side on the host,
including the chat UI (`ui/`). The remote writes config and secrets, and
chats through the bucket from outside the host's network.

It ships as `garage remote`, a subcommand of the same binary, so there's one
build and one release. The code lives here and is kept apart from host code.

## What it does

- **Mail** (experimental, chat only for now): reads `mail/to-laptop/` and
  writes `mail/to-host/` through the budgeted bucket client. It polls every
  10s, and only while the remote is running.
- **Keys**: holds the laptop's signing key, the host keys it trusts, and the
  master key's public recipient, all pinned locally. It encrypts secrets
  before writing them to the bucket, and it never holds the master private
  key.

*Built so far*, all in `cmd/garage/remote.go`:

- `garage remote secret NAME` encrypts stdin to the recipient in
  `~/.garage/recipient` and writes it to the bucket.
- `garage remote config` checks a `config/garage.json` exactly as `serve` will
  read it, and only then writes it.
- `garage remote chat -room R` prints what's waiting in `mail/to-laptop/`,
  mails each stdin line to R, and checks again every 10s until stdin ends.
  Its cursors are `~/.garage/mail.json`.

*Postponed* (`.plans/ROADMAP.md`, Later): the laptop side of the door
(STUN, the signed "open the door" request, WireGuard).

## Rules

- The remote shares the bucket client, mail format, and signing with the host
  through top-level packages. It never keeps its own copy of the wire format.
- It must keep working when the host is down. Showing what's in the bucket
  and queueing mail are always possible.
