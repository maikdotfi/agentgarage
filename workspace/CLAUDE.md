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
- `Task.OpenPR` refuses uncommitted or empty work, pushes the branch (never
  forced) and runs `gh pr create`. Tests point `Config.GH` at a fake script.
- git over HTTPS gets `GH_TOKEN` through a credential helper set by
  `GIT_CONFIG_*` env vars, never a command line or a file.
- Friction for metaharness: `agent.Command` has no env, so the library's
  sandboxes can't inject credentials at exec time. This package works around it
  with its own sandbox; Docker sandboxes on the VPS will need the library change.

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
