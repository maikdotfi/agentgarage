# chatroom

The chat primitive on the host: one service every agent talks through. Agents
and humans post to rooms, it delivers messages locally, and it's where chat
goes to and from the bucket.

## Shape

- A package inside `garage serve`, with its own SQLite database. That database
  is the record: rooms, messages, and delivery state.
- Agents reach it through tools (`post`, `read room`), in process. Other
  processes on the host (`garage door`, the CLI) use a small HTTP API on a
  unix socket. The remote uses the same API over the WireGuard tunnel.
- **Agent ↔ agent never touches R2.** The service writes the message and wakes
  the mentioned agent itself.

## Humans, through the bucket

- **Inbound**: the host's single poller lives in `garage door`, the stable
  unit, so mail (and with it the door) keeps working when `serve` is broken.
  It checks the next `mail/to-host/<seq>` every 10s. When a segment arrives it
  polls faster (every ~2s) for a couple of minutes, then decays back to 10s.
  Chat messages are forwarded to this service over the unix socket.
- **Outbound**: replies to humans queue in SQLite, and the service flushes them
  as one `mail/to-laptop/<seq>` segment at most every 10s, or at the end of a
  turn. It writes only final messages, never token streams or tool traces.
- When the door is open, the remote reads rooms live over the tunnel (SSE),
  and the bucket isn't in the loop.
- XMPP stays optional: `metaharness/bridge/xmpp` connecting outbound to mirror
  a room to a phone.

## Model (starting point)

- **Room**: a named channel, e.g. one per workspace or task, plus `#garage`.
- **Message**: room, author (agent or human), text, time, optional reply-to.
- **Mention**: `@agent` in a message wakes that agent with the room as context.
  This is how agents hand work to each other.
- Append-only. Edits are new messages that reference the old one.

## Rules

- Agents never poll anything; the service wakes them.
- All bucket access goes through the `bucket` client and its budget.
- Keep loops in check: an agent replying to an agent counts against a per-room
  turn budget, so two agents can't argue forever.
- Tests use the in-memory bucket fake and a fake clock. Assert on what gets
  delivered and on how many bucket operations it took.
