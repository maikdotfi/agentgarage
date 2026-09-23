package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maikdotfi/agentgarage/metaharness/agent"
	"github.com/maikdotfi/agentgarage/workspace"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// origin makes a bare repo with one commit on main, standing in for GitHub.
func origin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	git(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(dir, "seed")
	git(t, dir, "clone", "-q", bare, seed)
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-qm", "first")
	git(t, seed, "push", "-q", "origin", "HEAD:main")
	return bare
}

// fakeGH is a gh that records its arguments and prints a PR URL.
func fakeGH(t *testing.T) (path, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	path = filepath.Join(dir, "gh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho https://github.com/example/repo/pull/7\n"
	os.WriteFile(path, []byte(script), 0o755)
	return path, argsFile
}

func open(t *testing.T, remote string) *workspace.Workspace {
	t.Helper()
	gh, _ := fakeGH(t)
	ws, err := workspace.Open(context.Background(), workspace.Config{
		Name:     "demo",
		Remote:   remote,
		Root:     t.TempDir(),
		Identity: workspace.Identity{Name: "garage-dev", Email: "dev@garage.invalid"},
		Env:      map[string]string{"GH_TOKEN": "ghp_hunter2"},
		GH:       gh,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func sh(t *testing.T, box agent.Sandbox, script string) agent.ExecResult {
	t.Helper()
	res, err := box.Exec(context.Background(), agent.Command{Cmd: "sh", Args: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestTasksNeverShareAWorkingCopy(t *testing.T) {
	ctx := context.Background()
	ws := open(t, origin(t))
	a, err := ws.Start(ctx, "task-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ws.Start(ctx, "task-b")
	if err != nil {
		t.Fatal(err)
	}

	sh(t, a.Sandbox(), "echo a > only-in-a.txt")
	if res := sh(t, b.Sandbox(), "cat README.md; test -e only-in-a.txt && echo LEAK"); res.Stdout != "hello\n" {
		t.Errorf("task-b sees %q, want only the checked-out README", res.Stdout)
	}
	if res := sh(t, a.Sandbox(), "git branch --show-current"); strings.TrimSpace(res.Stdout) != a.Branch() {
		t.Errorf("task-a is on %q, want %q", res.Stdout, a.Branch())
	}
	if a.Branch() == b.Branch() {
		t.Errorf("both tasks are on branch %q", a.Branch())
	}
}

func TestSandboxGetsCredentialsButNotTheGaragesEnvironment(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "bucket-secret")
	ws := open(t, origin(t))
	task, err := ws.Start(context.Background(), "env")
	if err != nil {
		t.Fatal(err)
	}

	res := sh(t, task.Sandbox(), `test -n "$GH_TOKEN" && echo has-token; echo "aws=$AWS_SECRET_ACCESS_KEY"`)
	if res.Stdout != "has-token\naws=\n" {
		t.Errorf("stdout = %q, want the token present and the bucket secret absent", res.Stdout)
	}
}

func TestSecretValuesAreRedactedFromOutput(t *testing.T) {
	ws := open(t, origin(t))
	task, _ := ws.Start(context.Background(), "leak")

	res := sh(t, task.Sandbox(), `echo "token is $GH_TOKEN"; echo "$GH_TOKEN" >&2`)
	if strings.Contains(res.Stdout+res.Stderr, "hunter2") {
		t.Errorf("output leaks the token: %q / %q", res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "[redacted GH_TOKEN]") {
		t.Errorf("stdout = %q, want the token replaced by its name", res.Stdout)
	}
}

func TestCommitsAreMadeAsTheWorkspaceIdentity(t *testing.T) {
	ws := open(t, origin(t))
	task, _ := ws.Start(context.Background(), "who")

	res := sh(t, task.Sandbox(), "echo x > x && git add x && git commit -qm x && git log -1 --format=%an/%ae")
	if strings.TrimSpace(res.Stdout) != "garage-dev/dev@garage.invalid" {
		t.Errorf("author = %q", res.Stdout)
	}
}

func TestOpenPRPushesTheBranchAndAsksGHForAPR(t *testing.T) {
	ctx := context.Background()
	remote := origin(t)
	gh, argsFile := fakeGH(t)
	ws, err := workspace.Open(ctx, workspace.Config{
		Name: "demo", Remote: remote, Root: t.TempDir(),
		Identity: workspace.Identity{Name: "garage-dev", Email: "dev@garage.invalid"},
		GH:       gh,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _ := ws.Start(ctx, "pr")
	sh(t, task.Sandbox(), "echo change >> README.md && git commit -qam 'change readme'")

	url, err := task.OpenPR(ctx, "Change the readme", "Because.")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/example/repo/pull/7" {
		t.Errorf("url = %q", url)
	}
	if got := git(t, remote, "log", "-1", "--format=%s", task.Branch()); got != "change readme" {
		t.Errorf("origin %s is at %q, want the task's commit", task.Branch(), got)
	}
	if got := git(t, remote, "log", "-1", "--format=%s", "main"); got != "first" {
		t.Errorf("origin main moved to %q", got)
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"pr\ncreate\n", "--head\n" + task.Branch() + "\n", "--base\nmain\n", "--title\nChange the readme\n", "--body\nBecause.\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("gh args missing %q:\n%s", want, args)
		}
	}
}

func TestOpenPRWithNothingCommittedFails(t *testing.T) {
	ctx := context.Background()
	ws := open(t, origin(t))
	task, _ := ws.Start(ctx, "empty")
	if _, err := task.OpenPR(ctx, "t", "b"); err == nil {
		t.Error("want an error when the branch has no commits")
	}

	sh(t, task.Sandbox(), "echo change >> README.md")
	if _, err := task.OpenPR(ctx, "t", "b"); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("err = %v, want it to say there are uncommitted changes", err)
	}
}

func TestCloseRemovesTheWorktreeAndKeepsThePushedBranch(t *testing.T) {
	ctx := context.Background()
	remote := origin(t)
	ws := open(t, remote)
	task, _ := ws.Start(ctx, "done")
	sh(t, task.Sandbox(), "echo change >> README.md && git commit -qam done")
	if _, err := task.OpenPR(ctx, "t", "b"); err != nil {
		t.Fatal(err)
	}

	if err := task.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(task.Dir()); !os.IsNotExist(err) {
		t.Errorf("worktree %s still exists", task.Dir())
	}
	git(t, remote, "rev-parse", "--verify", task.Branch())
}

func TestNewTasksStartFromTheLatestMain(t *testing.T) {
	ctx := context.Background()
	remote := origin(t)
	ws := open(t, remote)
	ws.Start(ctx, "before")

	other := filepath.Join(t.TempDir(), "other")
	git(t, "", "clone", "-q", remote, other)
	os.WriteFile(filepath.Join(other, "NEW.md"), []byte("new\n"), 0o644)
	git(t, other, "add", ".")
	git(t, other, "commit", "-qm", "second")
	git(t, other, "push", "-q", "origin", "HEAD:main")

	task, err := ws.Start(ctx, "after")
	if err != nil {
		t.Fatal(err)
	}
	if res := sh(t, task.Sandbox(), "cat NEW.md"); res.Stdout != "new\n" {
		t.Errorf("new task does not have the latest main: %q %q", res.Stdout, res.Stderr)
	}
}
