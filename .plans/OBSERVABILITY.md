# Observability: see what the agents did

## Direction

The agents' own databases already hold almost everything worth knowing: every
session, every turn, every tool call and its output, the model's reasoning,
token counts, and the room each task belongs to. Nothing shows them. The first
real failure on the host made that plain: a deploy's build broke, dev spent
four minutes finding out why, then ended its turn with reasoning and no text,
and the room saw nothing at all. Finding out meant copying `dev.db` off the
host and reading `agent_messages` with `sqlite3`.

So the plan is a page, not a pipeline: read the databases that are already
there, in the process that already owns them, and render them. No metrics
server, no tracing library, no log shipping. When the page shows us a question
the data can't answer, we add that data to the database, and only then.

## Goals

- From any room, one click to what the agent behind it actually did: each
  turn, each tool call with its input and output, its reasoning, and how the
  turn ended.
- Across agents, a glance at what is going on right now: who is mid-turn, in
  which room, since when, and on which model.
- Turns that end badly (an error, no answer, a refused tool) stand out, so
  nobody has to notice a silent room.
- Token use per session and per agent, so switching models (Ollama Cloud,
  Anthropic) can be compared on real work.

## Non-Goals

- No Prometheus, OpenTelemetry, or a second process. The page reads the
  SQLite files `serve` already has open.
- No new database. Whatever is missing gets written to the agent's own
  database, by the library, next to what it already writes.
- No writes from the page. It observes; it never edits a session or kv.
- No login, same as the chat UI: the LAN range the firewall allows is the
  boundary. Transcripts can hold anything a tool printed, so they are exactly
  as private as the chat.
- No charts library. Numbers in tables until a table stops being enough.

## What the databases hold today

Per agent (`agents/dev.db`, `agents/grug.db`), written by metaharness:

- `agent_sessions`: id, model, status, token counts (input, output,
  reasoning, cache), `created_at`, `updated_at`.
- `agent_messages`: the transcript, one row per message with `seq`, `role`
  and fantasy's `content_json`: text, reasoning, tool-call and tool-result
  parts.
- `agent_kv`: the garage's own keys. dev's `dev/rooms/<room>` maps a room to
  its task, which is the session id; grug keeps the heads it reviewed.
- `agent_notes`: memory notes, unused by dev and grug so far.

`agent.Store` already has `ListSessions` and `Load`, and `agentdb` has `List`
for kv. Step 1 needs no library change.

Known gaps, seen in the real `dev.db`:

- `created_at` on a message is when the session was saved, not when the
  message happened. Rows saved together share a timestamp, so a tool call's
  duration can't be read back.
- There is no turn record: where a turn began, what woke it (which message,
  from whom), and how it ended (answer, error, empty, cancelled).
- `reasoning_tokens` stays 0 on Ollama Cloud, while the reasoning text is
  there.
- Deploys, releases and restarts live in the bucket and the journal, not in
  any database.

## Step 1: the agents page, read-only

1. **`/agents`**: one row per agent: its model and endpoint (as `serve`
   logged them), sessions, total tokens, and whether it is mid-turn right
   now, in which room, since when. `serve` already counts busy agents for
   restarts; make that count say who and where.
2. **`/agents/{name}`**: its sessions, newest first, from `ListSessions`:
   the room (joined through dev's `dev/rooms/*` kv), model, status, message
   count, tokens, last activity.
3. **`/agents/{name}/sessions/{id}`**: the transcript. User prompts, then
   each assistant message with its reasoning folded away, tool calls with
   their input, and tool results (long output folded, errors marked). A
   session whose last assistant message has no text is flagged.
4. **Links both ways.** A room page links to the session behind it, and a
   session links back to its room.

Wiring: `ui` gets a read-only view of each agent's store from `serve`,
through a small interface in `ui` (list sessions, load one, list kv by
prefix), so handler tests use an in-memory turso store with a scripted
session in it. Same template rules as the chat: one shell, a page per file,
partials shared.

**Milestone F:** the silent deploy turn from 2026-09-26 is obvious on the
page: its tool calls, the failing test output, its reasoning, and a flag that
it ended without an answer.

## Step 2: fill the gaps in the data

Each one is a metaharness change, so each follows `metaharness/CLAUDE.md`
(the caller first, then the `public-api` skill).

5. **When things happened.** Messages get the time they were produced, not
   the time they were saved, so the transcript shows how long each model call
   and tool call took.
6. **Turns.** A turn row per `Run`: session, start, end, how it ended, and
   the tokens it used. The page's "mid-turn" and "ended badly" come from here
   instead of being worked out.
7. **Reasoning tokens.** Count them where the provider doesn't, or show them
   as unknown rather than 0.

## Step 3: live, and the rest of the garage

8. **Live.** SSE on the session page and `/agents`, as the rooms do, so a
   turn in flight shows its tool calls as they happen. The answer to "is it
   doing anything?" from 2026-09-25.
9. **The garage's own events.** Deploys, releases seen, restarts and
   refusals go into a small events table in the chatroom database, and show
   up on `/agents` and as a quiet line in the room.

## Open Questions

- Should the room itself show a "dev is working" line from the turn record,
  or is a link to the live session page enough?
- How long do transcripts stay? Databases grow with every tool output; daily
  snapshots go to the bucket. Probably fine for a long time; measure first.
- Is `agent_notes` worth showing before any agent uses it?
- Could grug read its own sessions, or dev's, through a tool? Agents that can
  look at their own record is where this could go after the page.
