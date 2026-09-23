# agents

The garage's own agents, assembled from `metaharness`. Each one is a
`chatroom.Handler`: joined under a name, woken by a mention, and it answers in
the room it was mentioned in.

## dev

- One room is one task: the first mention picks a workspace (the one the
  message names, or the only one there is; otherwise it asks), starts a
  worktree, and opens a session. Later mentions in that room continue both.
- Each turn's prompt is every message in the room since dev last read it,
  minus its own. Its final answer is posted back.
- Tools: bash and the file tools, run in the task's worktree, plus
  `open_pull_request`, which calls `workspace.Task.OpenPR` and posts the link.
- The system prompt is `dev.md`, embedded.
- *Not built yet:* the room → task mapping lives in memory, so a restart
  starts a fresh task in a room.

## Rules

- Tests drive an agent through the chatroom with `testutils.ScriptedModel`,
  a local bare repo and a fake `gh`, and assert on what lands in the room and
  the remote.
