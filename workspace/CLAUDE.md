# workspace

A workspace is where an agent does real work on a real repo: a git checkout,
the sandbox it runs in, and the credentials it may use. "Workspace" is the
noun; there is no other name for it anywhere in the code.

## Model (starting point)

- **Workspace**: one git remote (for example `github.com/maikdotfi/agentgarage`),
  cloned once on the host.
- **Worktree per task**: each piece of work gets its own `git worktree` and
  branch, so agents never share a working copy. It is mounted into that task's
  sandbox.
- **Credentials**: what a workspace's sandboxes get in their environment,
  such as `GH_TOKEN` for `gh` and git pushes over HTTPS.
- This repository is a workspace like any other. The garage improving itself
  is just agents working in the `agentgarage` workspace.

## How it works today

- `Open` clones `Remote` to `<Root>/<name>/repo` once, and fetches after that.
  `Start(id)` fetches again and adds `<Root>/<name>/tasks/<id>` on branch
  `garage/<id>` off the remote's default branch.
- `Task.Sandbox()` is the workspace's own `agent.Sandbox`: commands run in the
  worktree with a minimal environment (PATH, HOME, the commit identity, the
  credentials) and **none** of the garage's own, so the bucket token never
  reaches an agent. Credential values are redacted from all output.
- Toolchains come from the repo's `mise.toml`, one install per workspace.
  `Start` runs `mise install` in the new worktree, and the sandbox runs every
  command through `mise exec --`. Tasks share `<Root>/<name>/tools`
  (`MISE_DATA_DIR`) and `<Root>/<name>/cache` (Go, npm and mise caches); deps
  like `node_modules` stay per worktree. Only the repo's `mise.toml` counts:
  the env points mise's global config into the workspace (where there is
  none) and stops its search at `tasks/`, so the host user's own mise tools
  never leak in. Tests point `Config.Mise` at a fake;
  `GARAGE_TEST_MISE=1 go test ./workspace` runs the real mise on PATH
  against a clone of this repo (it downloads Go).
- `Task.OpenPR` refuses uncommitted or empty work, pushes the branch (never
  forced) and runs `gh pr create`, unless the branch has a PR open already;
  then the push is the update. Tests point `Config.GH` at a fake script.
- `Resume(id)` is a task `Start` made earlier, perhaps before a restart: its
  worktree and branch as they were left. `Merged(sha)` is the full sha of a
  commit on the remote's default branch, or an error; only a hex sha will
  do, so a stale local `main` can't stand in for it.
- A `Workspace` is shared by every agent. Its own commands (`run`: fetch,
  worktree add and remove, push, gh) hold one lock per workspace, because git
  refuses rather than waits on its lock files. Commands in a sandbox don't
  take it; an agent's own git is its business.
- For reviews: `PullRequest(url)` is `gh pr view`, `Checkout(id, pr)` is a
  worktree detached at the PR's head. Its own `config.worktree` sets
  `remote.origin.pushurl` to something unpushable, so pushes to origin fail,
  while repos its commands make (a test suite's) still push. `Comment(url,
  body)` is `gh pr comment`.
- git over HTTPS gets `GH_TOKEN` through a credential helper set by
  `GIT_CONFIG_*` env vars, never a command line or a file.
- Friction for metaharness: `agent.Command` has no env, so the library's
  sandboxes can't inject credentials at exec time. This package works around it
  with its own sandbox; any library sandbox we adopt later needs the change.

## Secrets

Prototype stakes, so keep it simple and don't leak:

- Secrets live in the bucket as `secrets/<name>.age`, encrypted to the master
  key (see `bucket/CLAUDE.md`). A workspace names the secrets it needs, and the
  garage decrypts them in memory.
- Use one GitHub bot account with a fine-grained token scoped to the
  workspace repos. Don't use a personal token.
- Secrets reach the sandbox as environment variables, and only at exec time.
  Never write them into the system prompt, tool output, logs, or a commit.
  Redact known secret values from tool output before the model sees it.

## Rules

- Agents push branches and open PRs. They never push to `main`, and nothing
  force-pushes.
- A finished task's worktree is removed; its branch lives on in the remote.
- Tests use a local bare repo as the remote, not GitHub.
