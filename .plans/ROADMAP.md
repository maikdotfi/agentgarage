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
- No setup scripts, Ansible, or cloud-init. `garage setup` is the only thing
  that configures a host.
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

**Milestone B:** the garage runs on the VPS, humans chat with it through the
bucket (or over SSH), and agents open PRs here. The garage now works on itself.

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

- ~~Does `tursogo` support `VACUUM INTO`?~~ Yes (0.7.2), with a literal path
  only, and it doesn't unescape `''` in one. The snapshot opens in stock
  `sqlite3`.
- The dev agent needs Go on the host to run this repo's tests. Debian's is
  too old for `go 1.26`, so setup will have to install the official tarball.
- Does the `metaharness/bridge/xmpp` mirror earn its place, or does the remote
  cover phones well enough?
- When, if ever, can an agent deploy without a human merging to `main` first?
