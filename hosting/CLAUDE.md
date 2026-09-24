# hosting

One cheap VPS runs the `garage` binary. It is **fully private**: nothing
listens on the public internet. The host reaches out (to R2, model APIs,
GitHub); nothing reaches in except through a tunnel it agreed to open.

## Shape

- One binary, several subcommands, one systemd unit each:
  - `garage serve`: agents, chatroom, and the HTTP API the remote uses.
    Changes often. It renders no UI; that's the remote's job.
  - `garage door`: the WireGuard tunnel and SSH access. Changes rarely, so an
    app crash or a bad release never locks us out. Once it exists, the host's
    single bucket poller moves here from `serve`.
- No Docker on the host. Agents run in the workspace's own sandbox, a
  worktree and a minimal environment, as the unprivileged `garage` user; that
  user is the only boundary. Sandboxing gets re-thought once agents run here
  (`.plans/ROADMAP.md`), and plugs in behind `agent.Sandbox`.
- `garage backup`, run daily by a systemd timer, takes a consistent snapshot
  of every SQLite database (agents' and the garage's; never a raw copy of a
  live file) and uploads it under that owner's prefix. `garage restore` is
  the other half; test it.
- Break-glass access is the VPS provider's web console. Never remove it.

## Deploys: through the bucket, not gitops

Humans release from the laptop; agents release from the host (next section).
Both end in the same place.

1. `garage remote release` runs `go build` for linux/amd64, signs the
   result, uploads `releases/<sha>/garage`, and CAS-updates
   `releases/current`.
2. The host's poller GETs `releases/current` on the same tick as mail, so
   there's no extra poller. On a new sha it downloads, verifies the
   signature, swaps the binary, and lets systemd restart it.
3. Rollback means pointing `releases/current` back at an older sha.

## Inception: agents deploy the garage they run in

An agent on the host can ship a new version of the garage, including one it
wrote itself. It does this with a `deploy` tool, and the garage does the
risky parts:

1. The agent names a commit sha on `main`. Merging to `main` is the gate, and
   that stays a human's call until we choose otherwise.
2. The garage builds that sha itself in a fresh worktree (`go test ./...`, then
   `go build`). It never trusts a binary an agent hands it.
3. It signs the result with the host's release key, uploads it to
   `releases/<sha>/`, CAS-updates `releases/current`, and restarts
   `garage serve` into it.
4. **`garage door` is the watchdog.** If the new `serve` isn't healthy within
   a minute, the door points `serve` back at the previous release and posts
   to `#garage`.
5. The deploying agent's session lives in its database, so it survives the
   restart. On boot, the garage posts "running <sha>" to the room, which wakes
   the agent to check its own work.

Binaries live at `/opt/garage/releases/<sha>/garage`. `serve` runs whatever
the `serve-current` symlink points to, and `door` runs `door-current`. **Agents
only ever move `serve-current`.** The door is updated by a human, so a bad
release can't take away the way back in.

## The door: WireGuard, bootstrapped through bucket mail

Mail is experimental and carries only chat for now (`bucket/CLAUDE.md`). The
handshake below is what it has to grow into.

1. The laptop learns its public UDP endpoint via STUN and sends a signed "open
   the door" mail message (wg pubkey, endpoint, short expiry).
2. The host's poller, by then in `garage door`, receives it, and the door adds
   the peer and replies over mail with its own endpoint. Both sides then send
   UDP at each other to punch through (the host firewall is stateful, so its
   own outbound opens the way).
3. If punching fails (symmetric NAT), the host opens its WireGuard port to the
   laptop's observed IP only, until the request expires.
4. The remote's API calls and SSH run over the tunnel. The bucket only carries the handshake.

The boring fallback, if this ever eats more time than it saves, is Tailscale
or Headscale. We know, and we're doing it anyway.

## Rules

- The host firewall drops all unsolicited inbound traffic. Any exception is
  opened by the door, is scoped to one IP, and expires.
- Databases are local files; the bucket holds their daily snapshots. A dead
  disk loses at most a day, and that's accepted.
- Host setup is `garage setup`: idempotent Go in this folder, with the
  systemd units and firewall rules embedded. No scripts, no config management.
