package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maikdotfi/agentgarage/workspace"
)

// openPR is a workspace with a task that has opened a PR with one commit, and
// the fake gh's log of arguments.
func openPR(t *testing.T) (ws *workspace.Workspace, task *workspace.Task, url, remote, ghArgs string) {
	t.Helper()
	ctx := context.Background()
	remote = origin(t)
	gh, ghArgs := fakeGH(t)
	mise, _ := fakeMise(t)
	ws, err := workspace.Open(ctx, workspace.Config{
		Name: "demo", Remote: remote, Root: t.TempDir(),
		Identity: workspace.Identity{Name: "garage-dev", Email: "dev@garage.invalid"},
		Env:      map[string]string{"GH_TOKEN": "ghp_hunter2"},
		GH:       gh, Mise: mise,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _ = ws.Start(ctx, "pr")
	sh(t, task.Sandbox(), "echo change >> README.md && git commit -qam 'change readme'")
	if url, err = task.OpenPR(ctx, "Change the readme", "Because."); err != nil {
		t.Fatal(err)
	}
	return ws, task, url, remote, ghArgs
}

func TestOpenPRAgainPushesToThePRThatIsOpen(t *testing.T) {
	_, task, url, remote, ghArgs := openPR(t)
	sh(t, task.Sandbox(), "echo more >> README.md && git commit -qam 'answer review'")

	again, err := task.OpenPR(context.Background(), "Change the readme", "Because.")
	if err != nil {
		t.Fatal(err)
	}
	if again != url {
		t.Errorf("second OpenPR = %q, want the open PR %q", again, url)
	}
	if got := git(t, remote, "log", "-1", "--format=%s", task.Branch()); got != "answer review" {
		t.Errorf("origin %s is at %q, want the new commit", task.Branch(), got)
	}
	args, _ := os.ReadFile(ghArgs)
	if n := strings.Count(string(args), "pr\ncreate\n"); n != 1 {
		t.Errorf("gh pr create ran %d times, want once:\n%s", n, args)
	}
}

func TestCheckoutIsThePRHeadInAWorktreeOfItsOwn(t *testing.T) {
	ctx := context.Background()
	ws, task, url, remote, _ := openPR(t)

	pr, err := ws.PullRequest(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	head := git(t, remote, "rev-parse", task.Branch())
	if pr.URL != url || pr.Head != task.Branch() || pr.HeadSHA != head || pr.Base != "main" {
		t.Errorf("pr = %+v, want %s at %s into main", pr, task.Branch(), head)
	}

	review, err := ws.Checkout(ctx, "review", pr)
	if err != nil {
		t.Fatal(err)
	}
	if review.Dir() == task.Dir() {
		t.Fatal("the review shares the task's worktree")
	}
	if got := strings.TrimSpace(sh(t, review.Sandbox(), "git rev-parse HEAD").Stdout); got != head {
		t.Errorf("review is at %s, want the PR head %s", got, head)
	}
	diff := sh(t, review.Sandbox(), "git diff origin/"+pr.Base+"...HEAD").Stdout
	if !strings.Contains(diff, "+change") {
		t.Errorf("diff against the base = %q, want the PR's change", diff)
	}
}

func TestACheckoutCannotPush(t *testing.T) {
	ctx := context.Background()
	ws, _, url, remote, _ := openPR(t)
	pr, _ := ws.PullRequest(ctx, url)
	review, err := ws.Checkout(ctx, "review", pr)
	if err != nil {
		t.Fatal(err)
	}

	res := sh(t, review.Sandbox(), "git commit -q --allow-empty -m sneaky && git push -q origin HEAD:refs/heads/sneaky")
	if res.ExitCode == 0 {
		t.Error("git push from a checkout succeeded")
	}
	if out := git(t, remote, "branch", "--list", "sneaky"); out != "" {
		t.Errorf("origin has the pushed branch %q", out)
	}
}

func TestCheckoutInstallsTheRepoStack(t *testing.T) {
	ctx := context.Background()
	ws, _, url, _, _ := openPR(t)
	pr, _ := ws.PullRequest(ctx, url)
	review, err := ws.Checkout(ctx, "review", pr)
	if err != nil {
		t.Fatal(err)
	}
	if res := sh(t, review.Sandbox(), `echo "$TOOLCHAIN"`); res.Stdout != "mise\n" {
		t.Errorf("stdout = %q, want commands run through mise exec", res.Stdout)
	}
	if _, err := os.Stat(filepath.Join(review.Dir(), "README.md")); err != nil {
		t.Error(err)
	}
}

func TestCommentPostsOnThePR(t *testing.T) {
	ws, _, url, _, ghArgs := openPR(t)

	if err := ws.Comment(context.Background(), url, "grug say: good"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(ghArgs)
	if want := "pr\ncomment\n" + url + "\n--body\ngrug say: good\n"; !strings.Contains(string(args), want) {
		t.Errorf("gh args missing %q:\n%s", want, args)
	}
}

func TestPullRequestThatGHCannotFindIsAnError(t *testing.T) {
	gh, _ := fakeGH(t)
	mise, _ := fakeMise(t)
	ws, err := workspace.Open(context.Background(), workspace.Config{
		Name: "demo", Remote: origin(t), Root: t.TempDir(), GH: gh, Mise: mise,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.PullRequest(context.Background(), "https://github.com/example/repo/pull/99"); err == nil {
		t.Error("want an error for a PR gh doesn't know")
	}
}
