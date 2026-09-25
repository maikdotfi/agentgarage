// Package workspace is where agents do real work on a real repo: one clone per
// workspace, and a git worktree on its own branch per task. Credentials reach a
// task's commands as environment variables at exec time and never its output.
// Commands run through mise, with the stack the repo pins in its mise.toml
// installed once per workspace and shared by its tasks.
package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/maikdotfi/agentgarage/metaharness/agent"
)

// Identity is who a workspace's commits are by.
type Identity struct {
	Name  string
	Email string
}

// Config is one workspace.
type Config struct {
	Name     string // plain name, e.g. "agentgarage"
	Remote   string // what to clone, e.g. https://github.com/maikdotfi/agentgarage
	Root     string // host directory the clone and worktrees live under
	Identity Identity
	// Env is the credentials a task's commands get, such as GH_TOKEN. Their
	// values are redacted from everything a command prints.
	Env map[string]string
	// GH is the gh binary; empty means "gh" on PATH.
	GH string
	// Mise is the mise binary; empty means "mise" on PATH.
	Mise string
}

// Workspace is a cloned repo that hands out tasks. It is safe for concurrent
// use: the garage's own commands on the clone run one at a time.
type Workspace struct {
	cfg  Config
	repo string
	base string // the remote's default branch
	env  []string
	mu   sync.Mutex // held by run: git locks its own files and fails rather than waits
}

var plainName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Open clones the workspace's remote under Root, or fetches it if it is
// already there.
func Open(ctx context.Context, cfg Config) (*Workspace, error) {
	if !plainName.MatchString(cfg.Name) {
		return nil, fmt.Errorf("workspace: %q is not a plain name", cfg.Name)
	}
	if cfg.GH == "" {
		cfg.GH = "gh"
	}
	if cfg.Mise == "" {
		cfg.Mise = "mise"
	}
	ws := &Workspace{cfg: cfg, repo: filepath.Join(cfg.Root, cfg.Name, "repo"), env: environ(cfg)}
	if _, err := os.Stat(ws.repo); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(ws.repo), 0o755); err != nil {
			return nil, err
		}
		if _, err := ws.run(ctx, "", "git", "clone", "-q", cfg.Remote, ws.repo); err != nil {
			return nil, err
		}
	} else if _, err := ws.run(ctx, ws.repo, "git", "fetch", "-q", "origin"); err != nil {
		return nil, err
	}
	head, err := ws.run(ctx, ws.repo, "git", "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil {
		return nil, err
	}
	ws.base = strings.TrimPrefix(head, "origin/")
	return ws, nil
}

func (ws *Workspace) Name() string { return ws.cfg.Name }

// Start fetches the remote and gives the task its own worktree, on the new
// branch garage/<id> off the latest default branch, with the repo's stack
// installed.
func (ws *Workspace) Start(ctx context.Context, id string) (*Task, error) {
	if !plainName.MatchString(id) {
		return nil, fmt.Errorf("workspace: %q is not a plain task id", id)
	}
	if _, err := ws.run(ctx, ws.repo, "git", "fetch", "-q", "origin"); err != nil {
		return nil, err
	}
	t := &Task{ws: ws, id: id, branch: "garage/" + id, dir: filepath.Join(ws.cfg.Root, ws.cfg.Name, "tasks", id), env: ws.env}
	if _, err := ws.run(ctx, ws.repo, "git", "worktree", "add", "-q", "-b", t.branch, t.dir, "origin/"+ws.base); err != nil {
		return nil, err
	}
	if _, err := ws.run(ctx, t.dir, ws.cfg.Mise, "install"); err != nil {
		return nil, err
	}
	return t, nil
}

// Resume is a task Start made earlier, perhaps in another process, with its
// worktree as it was left.
func (ws *Workspace) Resume(id string) (*Task, error) {
	if !plainName.MatchString(id) {
		return nil, fmt.Errorf("workspace: %q is not a plain task id", id)
	}
	t := &Task{ws: ws, id: id, branch: "garage/" + id, dir: filepath.Join(ws.cfg.Root, ws.cfg.Name, "tasks", id), env: ws.env}
	if _, err := os.Stat(t.dir); err != nil {
		return nil, fmt.Errorf("workspace: no worktree for task %s: %w", id, err)
	}
	return t, nil
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Merged is the full sha of the commit rev abbreviates, if it is on the
// remote's default branch. Only a sha will do, not a branch name.
func (ws *Workspace) Merged(ctx context.Context, rev string) (string, error) {
	if !commitSHA.MatchString(rev) {
		return "", fmt.Errorf("workspace: %q is not a commit", rev)
	}
	if _, err := ws.run(ctx, ws.repo, "git", "fetch", "-q", "origin"); err != nil {
		return "", err
	}
	sha, err := ws.run(ctx, ws.repo, "git", "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("workspace: %s is not a commit here", rev)
	}
	if _, err := ws.run(ctx, ws.repo, "git", "merge-base", "--is-ancestor", sha, "origin/"+ws.base); err != nil {
		return "", fmt.Errorf("workspace: %s is not on %s", rev, ws.base)
	}
	return sha, nil
}

// PullRequest is a PR on the workspace's remote, as gh sees it.
type PullRequest struct {
	URL     string `json:"url"`
	State   string `json:"state"` // OPEN, CLOSED or MERGED
	Head    string `json:"headRefName"`
	HeadSHA string `json:"headRefOid"`
	Base    string `json:"baseRefName"`
}

// PullRequest looks up a PR by its URL.
func (ws *Workspace) PullRequest(ctx context.Context, url string) (PullRequest, error) {
	return ws.pullRequest(ctx, ws.repo, url)
}

// pullRequest is gh pr view of ref (a URL, number or branch) from dir.
func (ws *Workspace) pullRequest(ctx context.Context, dir, ref string) (PullRequest, error) {
	out, err := ws.run(ctx, dir, ws.cfg.GH, "pr", "view", ref, "--json", "url,state,headRefName,headRefOid,baseRefName")
	if err != nil {
		return PullRequest{}, err
	}
	var pr PullRequest
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return PullRequest{}, fmt.Errorf("gh pr view %s: %w", ref, err)
	}
	return pr, nil
}

// Checkout gives a task a look at a PR: its own worktree, detached at the PR's
// head, with the repo's stack installed. Pushes from it fail.
func (ws *Workspace) Checkout(ctx context.Context, id string, pr PullRequest) (*Task, error) {
	if !plainName.MatchString(id) {
		return nil, fmt.Errorf("workspace: %q is not a plain task id", id)
	}
	if _, err := ws.run(ctx, ws.repo, "git", "fetch", "-q", "origin"); err != nil {
		return nil, err
	}
	t := &Task{ws: ws, id: id, branch: pr.Head, dir: filepath.Join(ws.cfg.Root, ws.cfg.Name, "tasks", id),
		env: environ(ws.cfg, "remote.origin.pushurl", "read-only://checkouts-do-not-push")}
	if _, err := ws.run(ctx, ws.repo, "git", "worktree", "add", "-q", "--detach", t.dir, pr.HeadSHA); err != nil {
		return nil, err
	}
	if _, err := ws.run(ctx, t.dir, ws.cfg.Mise, "install"); err != nil {
		return nil, err
	}
	return t, nil
}

// Comment posts body as a comment on the PR at url.
func (ws *Workspace) Comment(ctx context.Context, url, body string) error {
	_, err := ws.run(ctx, ws.repo, ws.cfg.GH, "pr", "comment", url, "--body", body)
	return err
}

// Task is one piece of work: a worktree on its own branch.
type Task struct {
	ws     *Workspace
	id     string
	branch string
	dir    string
	env    []string
}

func (t *Task) Branch() string { return t.branch }
func (t *Task) Dir() string    { return t.dir }

// Sandbox runs commands in the task's worktree, through mise exec, with only
// the workspace's credentials, identity, tools and caches in their environment.
func (t *Task) Sandbox() agent.Sandbox { return sandbox{t} }

// OpenPR pushes the task's branch and opens a PR against the default branch,
// returning its URL. If the branch has a PR open already, the push updates it.
// Everything must be committed first.
func (t *Task) OpenPR(ctx context.Context, title, body string) (string, error) {
	status, err := t.ws.run(ctx, t.dir, "git", "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", fmt.Errorf("uncommitted changes, commit them first:\n%s", status)
	}
	ahead, err := t.ws.run(ctx, t.dir, "git", "rev-list", "--count", "origin/"+t.ws.base+"..HEAD")
	if err != nil {
		return "", err
	}
	if ahead == "0" {
		return "", fmt.Errorf("nothing committed on %s yet", t.branch)
	}
	if _, err := t.ws.run(ctx, t.dir, "git", "push", "-q", "origin", "HEAD:refs/heads/"+t.branch); err != nil {
		return "", err
	}
	if pr, err := t.ws.pullRequest(ctx, t.dir, t.branch); err == nil && pr.State == "OPEN" {
		return pr.URL, nil
	}
	out, err := t.ws.run(ctx, t.dir, t.ws.cfg.GH, "pr", "create",
		"--head", t.branch, "--base", t.ws.base, "--title", title, "--body", body)
	if err != nil {
		return "", err
	}
	lines := strings.Split(out, "\n")
	return lines[len(lines)-1], nil
}

// Close removes the worktree. Its branch lives on in the remote.
func (t *Task) Close() error {
	_, err := t.ws.run(context.Background(), t.ws.repo, "git", "worktree", "remove", "--force", t.dir)
	return err
}

// run is for the garage's own commands: it fails on a non-zero exit and
// returns trimmed, redacted stdout.
func (ws *Workspace) run(ctx context.Context, dir, name string, args ...string) (string, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, ws.env, &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, args[0], err, ws.redact(stderr.String()))
	}
	return strings.TrimSpace(ws.redact(stdout.String())), nil
}

func (ws *Workspace) redact(s string) string {
	for name, value := range ws.cfg.Env {
		if len(value) >= 4 {
			s = strings.ReplaceAll(s, value, "[redacted "+name+"]")
		}
	}
	return s
}

// environ is everything a workspace's commands see: enough of the host to run
// tools, the shared tools and caches, the commit identity, the credentials and
// any git config given as key, value pairs. Nothing else of the garage's
// environment, such as its bucket token, gets through.
func environ(cfg Config, gitConfig ...string) []string {
	root := filepath.Join(cfg.Root, cfg.Name)
	cache := filepath.Join(root, "cache")
	var env []string
	for _, k := range []string{"PATH", "HOME", "USER", "TMPDIR", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"MISE_DATA_DIR="+filepath.Join(root, "tools"),
		"MISE_CACHE_DIR="+filepath.Join(cache, "mise"),
		"MISE_TRUSTED_CONFIG_PATHS="+root,
		// Only the repo's mise.toml counts: not the host user's global
		// config, nor any mise.toml above the worktrees.
		"MISE_CONFIG_DIR="+filepath.Join(root, "mise-config"),
		"MISE_GLOBAL_CONFIG_FILE="+filepath.Join(root, "mise-config", "config.toml"),
		"MISE_CEILING_PATHS="+filepath.Join(root, "tasks"),
		"GOMODCACHE="+filepath.Join(cache, "gomod"),
		"GOCACHE="+filepath.Join(cache, "gobuild"),
		"npm_config_cache="+filepath.Join(cache, "npm"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME="+cfg.Identity.Name, "GIT_AUTHOR_EMAIL="+cfg.Identity.Email,
		"GIT_COMMITTER_NAME="+cfg.Identity.Name, "GIT_COMMITTER_EMAIL="+cfg.Identity.Email,
	)
	if _, ok := cfg.Env["GH_TOKEN"]; ok {
		// git over HTTPS reads the token from the environment, never from a
		// command line or a file.
		gitConfig = append(gitConfig, "credential.helper", `!f() { echo username=x-access-token; echo "password=$GH_TOKEN"; }; f`)
	}
	if n := len(gitConfig) / 2; n > 0 {
		env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", n))
		for i := range n {
			env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, gitConfig[2*i]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, gitConfig[2*i+1]))
		}
	}
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// sandbox is a task's worktree as an agent.Sandbox. Like metaharness's local
// sandbox it isolates nothing; it sets where commands run and what they see.
type sandbox struct{ t *Task }

func (s sandbox) Name() string { return s.t.id }
func (s sandbox) Close() error { return nil }

func (s sandbox) Exec(ctx context.Context, c agent.Command) (agent.ExecResult, error) {
	var stdout, stderr bytes.Buffer
	args := append([]string{"exec", "--", c.Cmd}, c.Args...)
	cmd := exec.CommandContext(ctx, s.t.ws.cfg.Mise, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = s.t.dir, s.t.env, &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return agent.ExecResult{}, err
		}
	}
	return agent.ExecResult{
		Stdout:   s.t.ws.redact(stdout.String()),
		Stderr:   s.t.ws.redact(stderr.String()),
		ExitCode: cmd.ProcessState.ExitCode(),
	}, nil
}
