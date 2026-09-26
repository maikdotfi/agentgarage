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
  postponed (`.plans/FOUNDATION.md`, Later) and plugs in behind `agent.Sandbox`.
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
  are missing, installs itself as `/opt/garage/releases/<content sha>/garage`
  and makes it `releases/current` (below), writes the units in `units/`,
  makes sshd key-only (`sshd -t` first) and loads `nftables.conf` (`nft -c` first), which opens 22 and the UI's 8080 to
  the `-ssh-from` range only. The port is fixed there; a `serve -http` on
  another port stays closed. Only a changed unit or binary
  restarts anything.
- Every privileged command goes through `Host.Run`, and every path through
  `Host.Root`, so tests use a pretend machine. `Exec` is the real thing.
- Host layout: `/etc/garage` (the keys, `trusted.keys`, `r2.env`, and the optional `serve.env` that picks the agents' models) and
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
- Releases (`release.go`), laid out so the garage user can install the next
  one itself and nothing more:
  ```
  /opt/garage/                  root's, 0755
    serve-current -> releases/current   root's link; systemd runs it; never moves
    releases/                   the garage user's (setup chowns it)
      current -> <sha>/garage   what serve runs: setup and the poller move it
      <sha>/garage              one per release, kept for rolling back
      seen                      the last releases/current the poller acted on
      deployed                  {sha, room} from a deploy, until the boot after it
  ```
  **Rerun `garage setup` once** on a host set up before this: it chowns
  `releases/`, adds `releases/current`, and points `serve-current` at it.
- *Not built yet:* the door unit.

## Deploys: through the bucket, not gitops

Humans release from the laptop; agents release from the host (next section).
Both end in the same place.

1. `garage remote release`, in a clean checkout, runs `go build` for
   linux/amd64 (`CGO_ENABLED=0`), uploads `releases/<commit sha>/garage`
   signed with the laptop's key (which the host trusts through
   `trusted.keys`), and CAS-updates `releases/current` (its body is the sha).
   `hosting.Publish` is the shared half.
2. The host's poller (`hosting.Update`, in `serve`) GETs `releases/current`
   on the same tick as mail, so there's no extra poller, and acts only when
   the pointer has moved since its last look (`seen`); its very first look
   only remembers. It downloads the binary, which the bucket client verifies,
   checks that `<new> help` runs here, flips `releases/current`, and once no
   agent is mid-turn `serve` exits 0; `Restart=always` brings it back up in
   the new binary after `RestartSec`. A release that fails is tried once and
   logged, then left alone until the next.
3. Rollback: `garage remote release -point <older sha>` moves the pointer
   back; it checks the release is in the bucket, and the host, which still
   has that binary on disk, downloads nothing. Or by
   hand on the host: `garage setup` with the old binary. Setup doesn't touch
   `seen`, so the poller leaves it be until the next release.

## Inception: agents deploy the garage they run in

An agent on the host can ship a new version of the garage, including one it
wrote itself. It does this with a `deploy` tool, and the garage does the
risky parts:

1. The agent names a commit sha on `main`. Merging to `main` is the gate, and
   that stays a human's call until we choose otherwise. Only a human's
   mention may deploy (`agents/CLAUDE.md`), so no notice can start another.
2. The garage builds that sha itself (`hosting.Deploy`) in a fresh detached
   worktree of the `agentgarage` workspace, through `mise exec`: `go test
   ./...`, then a linux/amd64 `go build ./cmd/garage`. It never trusts a
   binary an agent hands it, and refuses the sha it runs already.
3. It signs the result with the host's key (`signing.key`; there is no
   separate release key), uploads it to `releases/<sha>/`, CAS-updates
   `releases/current`, and the poller in the same process takes it from
   there, as for a laptop release.
4. **`garage door` is the watchdog** (once it exists; until then there is
   none, and rolling back means `garage setup` with the old binary). If the new `serve` isn't healthy within
   a minute, the door points `serve` back at the previous release and posts
   to `#garage`.
5. The deploying agent's session lives in its database, so it survives the
   restart. Deploy leaves `{sha, room}` in `releases/deployed`; on boot,
   after its first look at the bucket, `serve` posts "@dev running <sha>" to
   that room as `garage`, once, which wakes dev to check its own work.

Binaries live at `/opt/garage/releases/<sha>/garage`. `serve` runs
`serve-current`, which is root's and points at the garage user's
`releases/current`; `door` will run `door-current`. **Agents only ever move
`releases/current`.** The door is updated by a human, so a bad release can't
take away the way back in: its binary must live outside `releases/`, which
the garage user can write.

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
