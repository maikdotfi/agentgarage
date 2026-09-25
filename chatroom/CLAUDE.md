# chatroom

The chat primitive on the host: one service every agent talks through. Agents
and humans post to rooms, it delivers messages locally, and it's where chat
goes to and from the bucket.

## Shape

- A package inside `garage serve`, with its own SQLite database. That database
  is the record: rooms, messages, and delivery state.
- Agents reach it through tools (`post`, `read room`), in process. Other
  processes on the host (the CLI) use a small HTTP API on a unix socket. The
  UI (`ui/`) runs inside `garage serve` and uses the chatroom in process.
- **Agent ↔ agent never touches R2.** The service writes the message and wakes
  the mentioned agent itself.

## Humans, from outside the network

Mail is experimental (`bucket/CLAUDE.md`): as small as it can be until agents
run on their own and we know what they need to say. On the private network
humans use the UI or `garage chat` over SSH instead, and neither needs mail.

- **Inbound**: the host's single poller runs in `garage serve` until the door
  exists. It GETs the next `mail/to-host/<seq>` every 10s and posts each chat
  message to its room.
- **Outbound**: a final reply to a human is written as one
  `mail/to-laptop/<seq>` message, never token streams or tool traces.
- XMPP stays optional: `metaharness/bridge/xmpp` connecting outbound to mirror
  a room to a phone.

## Model (starting point)

- **Room**: a named channel, e.g. one per workspace or task, plus `#garage`.
- **Message**: room, author (agent or human), text, time, optional reply-to.
- **Mention**: `@agent` in a message wakes that agent with the room as context.
  This is how agents hand work to each other.
- Append-only. Edits are new messages that reference the old one.

## How it works today

- `Service` owns `chatroom.db` (one `messages` table). `Post` appends and
  wakes every mentioned, joined agent except the author; `Read` and `Wait`
  (a long poll) return what came after a message ID.
- `Rooms` is the latest message of every room, newest first (the UI's room
  list). A room exists once something is posted in it.
- `Join(name, handler)` gives an agent its own inbox; its handler runs one
  mention at a time.
- `Handler()` is the HTTP API on the unix socket: `POST /rooms/{room}/messages`
  and `GET /rooms/{room}/messages?after=N[&wait=30s]`.
- `RelayMail` is one pass of the mail bridge, run every 10s by `garage
  serve`: it posts what arrived in `mail/to-host/` (which wakes agents as usual),
  then mails agents' messages in rooms that mail came from to
  `mail/to-laptop/`. Humans' messages are never mailed back. Its cursors and
  those rooms are tables in `chatroom.db`.
- `Snapshot(path)` is `VACUUM INTO`, for `garage backup`. Turso takes only a
  literal path there and won't unescape `''`, so a path with a quote is refused.
- *Not built yet:* delivery state (a mention pending at shutdown is lost),
  reply-to, and the per-room turn budget.

## Rules

- Agents never poll anything; the service wakes them.
- All bucket access goes through the `bucket` client and its budget.
- Keep loops in check: an agent replying to an agent counts against a per-room
  turn budget, so two agents can't argue forever.
- Tests use the in-memory bucket fake and a fake clock. Assert on what gets
  delivered and on how many bucket operations it took.
