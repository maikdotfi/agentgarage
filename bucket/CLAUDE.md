# bucket

The R2 bucket is the garage's platform. The laptop's tools never talk to the
host directly: they talk only to the bucket. (On the host's private network a
human can also use the UI or SSH, which bypass the bucket.) All config,
secrets, durable state, backups, releases, and mail between laptop and host
live here.

**Every bucket operation costs real money.** This package is the only thing that
talks to S3, and it enforces a budget so nothing can spam it.

## The budgeted client

- Nothing outside this package imports the S3 SDK.
- Operations are counted by R2's billing class: Class A (writes, LIST, deletes)
  and Class B (GET, HEAD). Class A is the expensive one, so LIST is a
  write-priced operation and never goes in a loop.
- Each caller gets a named rate limit (a token bucket), e.g. `chat-poll`,
  `backup`, or `release`. A caller that runs out waits; it doesn't retry
  harder.
- Monthly totals per class are kept in SQLite. Past a soft limit, callers slow
  down and a message goes to `#garage`; past the hard limit, only backups may
  run. *Not built yet:* today `Client.Counts` is in memory only.
- Look up current R2 pricing before changing a limit; don't guess.

## What R2 gives us (and what it doesn't)

- Strong read-after-write consistency, so a GET after a PUT sees the PUT.
- Conditional writes (`If-None-Match: *`, `If-Match: <etag>`): create-only keys
  and compare-and-swap. This is our only locking primitive, and it is enough.
- No push. The host has a single poller (in `garage serve` until the door
  exists), and it polls known keys with GET, never LIST.
- It is not a database. SQLite runs on the host; the bucket holds snapshots.

## Secrets and the master key

- Secrets are the only thing we encrypt. Config, snapshots, mail, and files
  stay as they are; signing proves who wrote them, and that's enough.
- One secret is one object, `secrets/<name>.age`, encrypted with
  [age](https://pkg.go.dev/filippo.io/age) to the master key's public
  recipient.
- **The master key never goes in the bucket.** The private key lives on the
  host (`/etc/garage/master.key`, 0600), with one offline copy in a password
  manager for rebuilding a dead host. Writing a secret needs only the public
  recipient, so the laptop never holds the private key.
- Decrypted secrets live only in the garage's memory. They reach sandboxes as
  environment variables at exec time and are never written to disk, logs, or
  model context.
- **Rotating the master key means reissuing every secret**: generate a new
  key, create new upstream credentials (tokens, API keys), write them to the
  new recipient, and revoke the old ones. We never just re-encrypt, because
  anything the old key could open counts as leaked.

## Trust is pinned locally, never read from the bucket

Anyone with bucket write access could swap a public key stored there for their
own. So these are set on each machine, never read from the bucket:

- the host's list of which signing keys it accepts (the laptop's, plus its own
  release key for releases it built itself)
- the laptop's list of which signing keys it accepts (the host's)
- the master key's public recipient the laptop encrypts secrets to

## Key layout

```
agents/<name>/db/<day>.db      daily snapshot of that agent's database
agents/<name>/db/latest        pointer: the day of the newest snapshot
agents/<name>/files/           what the agent chooses to keep
garage/<db>/db/<day>.db        garage's own databases (chatroom), same scheme
config/garage.json             what garage serve runs: model, git identity, workspaces
secrets/<name>.age             age-encrypted to the master key; nothing else is
mail/to-host/<seq>             laptop -> host, one chat message
mail/to-laptop/<seq>           host -> laptop, same
releases/<sha>/garage          signed linux binaries
releases/current               pointer: which sha the host should run
```

- A restore GETs `latest` and then that day's snapshot: two Class B reads per
  database, never a LIST.

- **Mail is experimental**, kept as small as possible until agents run on
  their own and we know what they need to say. Then we redesign it.
- Mail is sequence-numbered and create-only, one message per object. A reader
  GETs the next number until it stops getting 404, with no cursor object and
  no LIST. Chat text is the only kind of message. `bucket/mail` is the wire
  format; forged or malformed mail is skipped, and a sender that is behind
  takes the next free number.
- Each side keeps its own cursors: the host in `chatroom.db` (so they are
  backed up), the laptop in `~/.garage/mail.json`. Once expired mail is gone,
  a side that loses its cursors can't find its place again. Accepted while
  mail is experimental.
- Mail is transport, not the record. Consumed messages expire through an R2
  lifecycle rule, and snapshots do too after a retention window.

## Rules

- **Everything written is signed** (ed25519, one key per side). Readers drop
  unsigned or badly signed objects. A leaked bucket token must not equal a
  shell on the host.
- The signature rides in object metadata (`x-amz-meta-garage-sig`) and covers
  the key as well as the body, so a signed object can't be replayed under
  another key. A side always trusts its own key, plus the pinned list.
- **Agents and sandboxes never hold bucket credentials.** Only the garage
  process does.
- **No plaintext secrets in the bucket, and nothing but secrets is
  encrypted.**
- Tests run against an in-memory fake that honours conditional writes and
  counts operations by class.
