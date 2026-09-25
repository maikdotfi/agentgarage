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
- `deploy`, only when the garage gives `DevConfig.Deploy` (on a host, with an
  `agentgarage` workspace): it takes a sha on main and hands it to the
  garage, which builds, tests and releases it (`hosting/CLAUDE.md`,
  "Inception"). It refuses unless the mention that woke the turn is a
  human's: not dev, grug, or `garage`, who posts "@dev running <sha>" on
  boot. So the boot notice wakes dev to check its work but can't deploy.
- The system prompt is `dev.md`, embedded.
- Its database (`DevConfig.Store`, a `turso.Store` in serve) keeps the
  sessions and, under `dev/rooms/<room>`, each room's workspace, task id and
  last message read. After a restart, a mention in the room resumes the task:
  `workspace.Resume` finds the worktree, the session is loaded and bound to
  it. If either is gone, dev says so and starts a new task.

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
- What grug reviewed lives in its own database (`agents/grug.db`), under
  `grug/reviewed/<pr url>`, so a restart doesn't review a head again.
- *Not built yet:* PRs from forks aren't fetched.

## Rules

- Tests drive an agent through the chatroom with `testutils.ScriptedModel`,
  a local bare repo and a fake `gh`, and assert on what lands in the room and
  the remote. A restart is a chatroom and a store on files, closed and
  opened again.
