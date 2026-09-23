# Agent Garage

A platform for building agents and getting real software work done with them —
including work on this repository. Agents here are users of the garage *and*
contributors to it, so everything we write is read by agents as often as by
humans.

## Ethos

Complexity is the apex predator. Grug-brained defaults:

- **Go, and boring Go.** Standard library first. A dependency must earn its place
  and gets one line of justification in the `STACK.md` of the package that pulls
  it in.
- **Server side, except the remote.** Everything is deployed on the host,
  except `remote/`, the one corner a human runs on their laptop.
- **Monolith, single binary.** One `garage` binary on one VPS. Assets (HTML,
  CSS, migrations, prompts) are `embed`ed. A second process or service needs a
  reason stronger than "that's how it's usually done".
- **Private by default.** Nothing listens on the public internet. The R2
  bucket is the hub: state, releases, backups, and laptop↔host mail live there,
  signed. See `bucket/CLAUDE.md`.
- **SQLite files, not database servers.** One SQLite-format file (Turso, as in
  metaharness) per owner: each agent, the chatroom. Snapshots go to the bucket.
- **No build step outside `go build`.** No npm, no codegen pipelines, no YAML
  towers.
- **Trusted context, prototype stakes.** Don't build security theatre, but don't
  leak secrets into model context, logs, or git either.
- **Delete before you add.** The smallest change that works; say no to
  speculative abstraction. Three similar lines beat a premature helper.

## Rules

- strict TDD: write the failing test first
- tests must test the behaviour, not the implementation
- stay true to idiomatic Go
- comments are allowed, but they must be short 1-2 sentences in most cases
  - use godoc style for comments, especially larger ones when required for an
    entire package
- every folder with its own concerns has its own `CLAUDE.md`; read it before
  working there, and keep it short and current when you change how things work

## Layout

```
metaharness/   the agent library (own Go module); has its own stricter rules
remote/        the only laptop-side code: `garage remote`, UI, mail, door
chatroom/      where agents and humans talk to each other
hosting/       how the garage runs privately on the VPS, deploys, the door
bucket/        R2 as the platform: layout, signing, conditional writes
workspace/     git repos, branches, gh, and secrets that agents work in
```

## metaharness is a library, the garage is its caller

The garage imports `metaharness`; `metaharness` never imports the garage. When
the garage wants something from the library, that is a public API change and
follows `metaharness/CLAUDE.md` (write the caller first, invoke the `public-api`
skill). Garage code is exactly the "somebody else's `main`" that library is
designed against, so friction felt here is a finding to report back there.
