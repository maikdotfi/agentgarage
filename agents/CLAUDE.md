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
  `open_pull_request`, which calls `workspace.Task.OpenPR` (a second call
  pushes to the PR already open) and posts the link with `@grug`.
- The system prompt is `dev.md`, embedded.
- *Not built yet:* the room → task mapping lives in memory, so a restart
  starts a fresh task in a room.

## grug

- The reviewer. Woken by a mention with a PR link (`…/pull/N`), it looks the
  PR up with gh, checks its head out in a worktree of its own
  (`workspace.Checkout`: detached, pushes fail), and runs one fresh session
  there. It reads the diff itself with `git diff origin/<base>...HEAD`.
- Tools: bash, `read_file`, and the `skill` tool with `skills.GrugReview()`
  from metaharness, so there is one copy of the skill. The system prompt is
  `grug.md`, embedded.
- Its final answer is the review: posted on the PR as a plain comment
  (`workspace.Comment`, gh pr comment; dev and grug share one bot account,
  and GitHub refuses approving or requesting changes on your own PR), then
  in the room, mentioning whoever asked. The worktree is removed afterwards.
- No ping-pong: grug reviews each PR head once, and a PR at most 3 times;
  after that a human takes over. Only a review mentions anyone; "no link",
  "already reviewed" and errors are plain messages that wake nobody. dev's
  prompt says not to mention `@grug` itself; re-opening the PR does it.
- *Not built yet:* what grug reviewed lives in memory, so a restart may
  review a head again. PRs from forks aren't fetched.

## Rules

- Tests drive an agent through the chatroom with `testutils.ScriptedModel`,
  a local bare repo and a fake `gh`, and assert on what lands in the room and
  the remote.
