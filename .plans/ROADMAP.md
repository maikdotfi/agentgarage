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
- The garage then moves to its own host. On a private LAN, humans reach it
  directly, over SSH and the UI; the bucket holds state, secrets and backups.
- Agents deploy the garage they run in (see `hosting/CLAUDE.md`, "Inception").
- Other software projects become just more workspaces.

## Non-Goals

- No UI before Milestone B. `garage chat` and `garage remote chat` are enough.
- No door for now. The host sits on a private LAN, and SSH and the UI are open
  to the LAN range only (`garage setup -ssh-from <range>`).
- No HTTPS or login on the UI. It is plain HTTP, reachable only from the
  range the firewall allows.
- No setup scripts, Ansible, or cloud-init. `garage setup` is the only thing
  that configures a host.
- No agents beyond dev and grug until Milestone E.
- No sandboxing for now. Agents run in their workspace's local folders, a
  worktree and a minimal environment as the `garage` user, which metaharness
  already supports. That is good enough; sandboxing waits (see Later).

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

6. **Mail, experimental.** Just enough for a human to chat from the laptop:
   one signed message per object at `mail/to-host/<seq>` and
   `mail/to-laptop/<seq>`, create-only, read by GETting the next number. Chat
   text is the only kind of message, and there's no batching. The poller
   lives in `garage serve` until the door exists. `garage remote chat` is a
   plain command-line tool. It's one small package marked experimental, and
   we redesign it once agents run on their own and we know what they need to
   say. `ssh host garage chat` works without it.
7. **`garage setup`.** The binary sets up its own host. No scripts: the only
   things copied over are the binary and the secrets it can't make itself.
   - `garage`, built with `GOOS=linux GOARCH=amd64 go build`
   - `/etc/garage/r2.env`, the bucket credentials
   - `/etc/garage/master.key` and `/etc/garage/signing.key`, only when
     rebuilding a host. Snapshots are signed by the host key, and the laptop
     pins it, so a rebuilt host keeps its old one.

   Then, as root, `garage setup -ssh-from <ip> -trust <laptop key>`. Each step
   checks the host first and does nothing if it's already right, so running
   it again is safe. It:
   - makes the unprivileged `garage` user and its directories
   - installs the packages the garage execs (git, gh); Debian only
   - installs `mise` from its GitHub release, version and sha256 pinned in
     the binary (see step 9)
   - makes the signing key and the master key if they're missing, and prints
     the public key and recipient to pin on the laptop
   - copies itself to `/opt/garage/releases/<sha>/garage` and points
     `serve-current` at it
   - writes its embedded systemd units, enables them, and restarts only what
     changed
   - drops unsolicited inbound traffic except SSH from the one IP, and makes
     sshd key-only; it checks the sshd config before reloading it

   After that it's up to `garage serve`, on every start: fetch config and
   secrets from the bucket, clone any missing workspaces, wake the agents.
   Until releases land, deploying by hand means copying a new binary and
   running `garage setup` again. No Docker. The `garage` user is the only
   boundary: an agent can do whatever that user can.
8. **`garage backup` / `garage restore`**, on a systemd timer that `garage
   setup` installs. On a host with no databases, setup restores the latest
   snapshots, so rebuilding a dead host means the same copies and the same
   command. Test a restore by doing exactly that.

*Built:* all three, tested against the in-memory bucket, and `garage setup`
twice in a Debian trixie container (real apt, useradd, nft, sshd; systemctl
stubbed). Not yet done on a real VPS, and neither is the restore drill.
`serve` also restores missing databases on every start, so setup needs no
bucket code of its own.

9. **Toolchains, one stack per workspace.** Debian's packages are too old
   for this repo (trixie ships Go 1.24, `go.mod` wants 1.26), and other
   workspaces will want Node and friends. Each workspace installs its stack
   once and every agent on it shares that install:
   ```
   <Root>/<name>/
     repo/          the clone
     tools/         MISE_DATA_DIR: go, node, … at the versions the repo pins
     cache/         GOMODCACHE, GOCACHE, npm_config_cache, MISE_CACHE_DIR
     tasks/<id>/    a worktree; per-checkout deps like node_modules live here
   ```
   - The repo declares its stack in `mise.toml` (this repo: `go`). The
     garage has no installers of its own.
   - `Start(id)` runs `mise install` in the new worktree. A version another
     task already installed costs nothing; a branch that bumps it gets the new
     one alongside the old.
   - `Task.Sandbox()` runs commands through `mise exec --` and adds the cache
     variables to its minimal env, with `MISE_TRUSTED_CONFIG_PATHS` set to the
     workspace. Dependency installs (`npm ci`, `go mod download`) are the
     agent's business, per worktree, fed by the shared cache.
   - System libraries (`build-essential` and the like) stay host-wide apt
     packages in `garage setup`. Nothing is installed per agent.

   Tests use a fake `mise` on PATH, like the fake `gh`.

*Built:* both halves, with fakes in the tests, and `mise install` / `mise
exec` checked by hand with the workspace's env against this repo's
`mise.toml`. Not yet run on the VPS.

**Milestone B:** the garage runs on the VPS, humans chat with it through the
bucket (or over SSH), and agents open PRs here. The garage now works on itself.

*Done* on a Debian box on the LAN (2026-09-25): setup, serve, chat over SSH
and mail, and a backup. `-ssh-from` now takes a range. No agent task has run
there yet, so no workspace has a `tools/` yet.

## Phase 3: chat with dev from the browser

The target: open the chat UI and start coding on agentgarage right away with
**dev**, the coding agent (built), working only on this repo.

10. **Toolchains on the host.** Step 9 for real: the first task on the host
    fills `<workspace>/tools` and `cache/` through the pinned `mise`, and
    `go test ./...` runs through `mise exec`. Fix whatever the real host
    turns up (PATH, trust, permissions of `/var/lib/garage`), so that every
    tool an agent uses lives in the workspace's `tools/`, not on the host.
11. **The chat UI, served by `garage serve`.** Plain HTTP on `0.0.0.0:8080`,
    no HTTPS, no login: the host is on a private network. As small as it can
    be: a room list, a room page, and a form to post. New messages show up
    through a plain htmx poll; no SSE yet. It talks to the chatroom in the
    same process, so no mail and no remote. `garage setup` opens 8080 to the
    same range as SSH, so on a public VPS it would still be private. Small
    enough to write by hand, since agents need it to be reachable at all.

**Milestone C:** open `http://<host>:8080`, "@dev do X in agentgarage", and a
PR appears, built and tested with the Go that mise put in `tools/`.

## Phase 4: grug reviews

The garage's second agent: **grug**, the code reviewer. One reviewer, not two.

12. **grug, the reviewer.** A second agent in `agents/`. When dev opens a PR
    it mentions grug in the room; grug reads the diff in its own worktree,
    reviews it with the `grug-review` skill (as in
    `metaharness/examples/code-review`), and posts the review on the PR and in
    the room. dev answers the findings; a human merges.
13. **Live rooms.** SSE replaces the poll, now that two agents talking makes
    it worth having.

**Milestone D:** every PR dev opens gets a grug review, and dev answers it.

## Phase 5: the garage ships itself

Once a human merges, the garage builds and deploys itself, so the agents
update the garage they run in. The riskiest phase, so it goes last, and dev
and grug help build it.

14. **dev remembers its tasks.** The room → task mapping moves from memory
    into dev's database, so a restart doesn't lose work in flight.
15. **Releases and inception.** Releases through the bucket, the agent
    `deploy` tool (the garage builds the merged sha itself: `go test ./...`,
    then `go build`), and versioned binaries with `serve-current`. Until the
    door exists there is no watchdog: rolling back means `garage setup` with
    the old binary, by hand.

**Milestone E:** a human merges, and the garage builds, deploys and restarts
into it without losing the room.

## Later

Postponed, not dropped. Each comes back when something real asks for it.

- **Sandboxing.** Decide from what agents actually do on the host: something
  of our own, or nothing more than the `garage` user. Whatever we choose
  plugs in behind `agent.Sandbox`. If it's a library sandbox, `agent.Command`
  needs an env first (see `workspace/CLAUDE.md`).
- **The door.** WireGuard bootstrapped through mail, and the watchdog with
  automatic rollback. A human writes this one; it's the piece that can lock
  us out. Needed once the garage leaves the LAN.
- **More agents.** Testing grug, then monitoring grug (watches the bucket
  budget, backups and releases, and posts to `#garage`). No new agent before
  dev and grug ship through Milestone E.
- **The remote over mail.** Mail stays as built (chat only). The UI doesn't
  use it; it comes back with the door, for reaching the garage from outside.

## First tasks for agents

The first real tasks should be small and well specified, so we learn how the
garage fails before it matters. For example: "add bucket operation counters to
`garage status`", not "build the UI".

## Open Questions

- ~~Does `tursogo` support `VACUUM INTO`?~~ Yes (0.7.2), with a literal path
  only, and it doesn't unescape `''` in one. The snapshot opens in stock
  `sqlite3`.
- ~~How does the dev agent get Go 1.26 on Debian?~~ Per-workspace toolchains
  through `mise` (step 9). Open: postinstall scripts run arbitrary code as the
  `garage` user; accepted until sandboxing comes back.
- Does the `metaharness/bridge/xmpp` mirror earn its place, or does the remote
  cover phones well enough? The UI on the LAN covers phones at home.
- When, if ever, can an agent deploy without a human merging to `main` first?
