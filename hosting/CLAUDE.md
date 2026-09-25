# hosting

One host runs the `garage` binary, today a Debian box on a private LAN. It
is **private**: nothing listens on the public internet. The host reaches out
(to R2, model APIs, GitHub); only SSH and the UI reach in, and only from the
range `garage setup -ssh-from` allows.

## Shape

- One binary, several subcommands, one systemd unit each:
  - `garage serve`: agents, the chatroom, and the chat UI (`ui/`) on
    `0.0.0.0:8080`, plain HTTP. Changes often.
  - `garage door`, *postponed* while the host is on a private LAN: the
    WireGuard tunnel and SSH access. Changes rarely, so an app crash or a bad
    release never locks us out. Once it exists, the host's single bucket
    poller moves here from `serve`.
- No Docker on the host. Agents run in the workspace's local folders, a
  worktree and a minimal environment, as the unprivileged `garage` user; that
  user is the only boundary, and that's good enough for now. Sandboxing is
  postponed (`.plans/ROADMAP.md`, Later) and plugs in behind `agent.Sandbox`.
- `garage backup`, run daily by a systemd timer, takes a consistent snapshot
  of every SQLite database (agents' and the garage's; never a raw copy of a
  live file) and uploads it under that owner's prefix. `garage restore` is
  the other half; test it.
- Break-glass access is the machine's own console (on a VPS, the provider's
  web console). Never remove it.

## How it works today

- `Setup` (`garage setup -ssh-from <ip or range> -trust <laptop key>`, as root on
  Debian) checks everything it can before touching anything: r2.env is there,
  someone has an `authorized_keys`, the keys parse. Then it makes the
  `garage` user, installs git, gh and ca-certificates, installs the pinned
  `mise` release as `/usr/local/bin/mise` (sha256 checked; bump `Mise` in
  `setup.go` to upgrade), makes whichever keys
  are missing, installs itself as `/opt/garage/releases/<content sha>/garage`,
  writes the units in `units/`, makes sshd key-only (`sshd -t` first) and
  loads `nftables.conf` (`nft -c` first). Only a changed unit or binary
  restarts anything.
- Every privileged command goes through `Host.Run`, and every path through
  `Host.Root`, so tests use a pretend machine. `Exec` is the real thing.
- Host layout: `/etc/garage` (the keys, `trusted.keys`, `r2.env`) and
  `/var/lib/garage` (databases, workspaces, the socket), both owned by
  `garage`, 0700. `cmd/garage` finds them by the existence of `/var/lib/garage`.
- Backups: `garage backup` asks `serve` over the socket (`POST /backup`),
  because only the process that owns a database can snapshot it
  (`VACUUM INTO`). `Backup` uploads `<owner>/db/<day>.db` and moves
  `<owner>/db/latest`. `serve` runs `Restore` on every start, and it fills in
  only databases that are missing. So a rebuilt host restores itself.
- Snapshots are signed by the host key, so **a rebuild needs `signing.key` as
  well as `master.key`**, or it can't verify its own backups and every
  laptop has to pin a new host key.
- There is no Go or Node on the host itself. Workspaces install what their
  repo pins through `mise` (`workspace/CLAUDE.md`).
- *Not built yet:* the door unit and releases through the bucket.

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
4. **`garage door` is the watchdog** (once it exists; until then there is
   none, and rolling back means `garage setup` with the old binary). If the new `serve` isn't healthy within
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

*Postponed* while the host is on a private LAN; needed once it leaves.

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

- The host firewall drops all unsolicited inbound traffic except SSH and the
  UI from the range `garage setup` allows. Any other exception is opened by
  the door, is scoped to one IP, and expires.
- Databases are local files; the bucket holds their daily snapshots. A dead
  disk loses at most a day, and that's accepted.
- Host setup is `garage setup`: idempotent Go in this folder, with the
  systemd units and firewall rules embedded. No scripts, no config management.
