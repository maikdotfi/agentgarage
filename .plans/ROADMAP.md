# Garage Roadmap

## Direction

Everything is ordered around one milestone: **the first PR against this repo
written by a garage agent.** From that point agents help build the rest, so
every step before it is the shortest path there, and everything that isn't on
that path waits.

That path needs four things: an agent, a workspace, a way to hand it a task,
and GitHub for review. The UI, the door, backups, and releases are not on it.
The PR itself is the output, and a human reviews it on GitHub.

The bucket tricks are sound and cheap; the door is the real rabbit hole. So the
door goes last, and nothing depends on it.

## Goals

- An agent opens a real PR on this repo as early as possible, even from a
  laptop.
- The garage then moves to the VPS, and humans reach it only through the
  bucket.
- Agents deploy the garage they run in (see `hosting/CLAUDE.md`, "Inception").
- Other software projects become just more workspaces.

## Non-Goals

- No UI before Milestone B. `garage chat` and `garage remote chat` are enough.
- No door before agents can deploy the garage. Until then, SSH stays open for
  key-based login from one IP, as a deliberate stopgap.
- No second agent before the first one has shipped a PR.
- No Docker sandboxes. On the VPS, agents run in the workspace's own
  sandbox: a worktree and a minimal environment, isolating nothing. We
  re-think sandboxing once agents are running there.

## Phase 1: laptop, one agent, one PR

1. **Skeleton.** A single root `go.mod` (`github.com/maikdotfi/agentgarage`)
   covering `metaharness` too, and `cmd/garage` with empty
   subcommands (`serve`, `door`, `remote`, `backup`, `restore`, `chat`).
2. **`bucket`.** The budgeted S3 client, operation counts by billing class,
   named rate limits, ed25519 signing, and an in-memory fake that honours
   conditional writes and counts operations. Everything rests on this.
3. **Secrets.** `secrets/<name>.age` and the master key file. It's needed
   right away for `GH_TOKEN` and the model API key.
4. **`workspace`.** Clone once, a git worktree and branch per task, the token
   injected at exec time, and `gh pr create` at the end. Tests use a local
   bare repo.
5. **`chatroom` core plus the dev agent.** Rooms and messages in SQLite, a
   mention wakes an agent, and `garage chat` talks over the unix socket.

**Milestone A:** `garage serve` on a laptop, "@dev do X in agentgarage", and a
PR appears.

## Phase 2: VPS, through the bucket

6. **Mail.** Signed, batched, sequence-numbered segments; the single poller in
   `garage door`; `garage remote chat` as a plain command-line tool.
7. **Deploy by hand.** `scp` the binary and add systemd units, plus the
   idempotent host setup script. Key-only SSH from one IP until the door lands.
   No Docker on the host. The garage runs as an unprivileged `garage` user,
   and that user is the only boundary: an agent can do whatever it can.
8. **`garage backup` / `garage restore`**, on a systemd timer. Test a restore.

**Milestone B:** the garage runs on the VPS, humans talk to it through R2,
and agents open PRs here. The garage now works on itself.

## Phase 3: agents help build the rest

9. **Re-think sandboxing.** Decide from what running agents actually do on
   the host: something of our own, or nothing more than the `garage` user.
   Whatever we choose plugs in behind `agent.Sandbox`. If it's a library
   sandbox, `agent.Command` needs an env first (see `workspace/CLAUDE.md`).
10. **Releases and inception.** Releases through the bucket, the agent `deploy`
    tool (the garage builds the sha itself), versioned binaries with
    `serve-current` / `door-current`, and the door as watchdog with automatic
    rollback.
11. **Club grug.** Reviews every PR with `grug-review`: the first check on
    agent-written code.
12. **Testing grug, then monitoring grug.** Monitoring watches the bucket
    budget, backups, and releases, and posts to `#garage`.
13. **The remote UI.** Mail-backed first, live over the tunnel later. A good
    task for the dev agent.
14. **The door.** WireGuard bootstrapped through mail. A human writes this
    one; it's the piece that can lock us out. Then SSH closes.

## First tasks for agents

The first real tasks should be small and well specified, so we learn how the
garage fails before it matters. For example: "add bucket operation counters to
`garage status`", not "build the UI".

## Open Questions

- Does `tursogo` support `VACUUM INTO` or an online backup API? If not,
  backups briefly pause writes to that one database.
- Does the `metaharness/bridge/xmpp` mirror earn its place, or does the remote
  cover phones well enough?
- When, if ever, can an agent deploy without a human merging to `main` first?
