package workspace_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/maikdotfi/agentgarage/workspace"
)

func TestAgentsCanUseOneWorkspaceAtTheSameTime(t *testing.T) {
	ctx := context.Background()
	ws := open(t, origin(t))
	pr := workspace.PullRequest{Head: "main", HeadSHA: "origin/main"}

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 8 {
		wg.Go(func() {
			task, err := ws.Start(ctx, fmt.Sprintf("task-%d", i))
			if err == nil {
				sh(t, task.Sandbox(), "git commit -q --allow-empty -m work")
				_, err = task.OpenPR(ctx, "work", "body")
			}
			errs <- err
		})
		wg.Go(func() {
			review, err := ws.Checkout(ctx, fmt.Sprintf("review-%d", i), pr)
			if err == nil {
				err = review.Close()
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestResumeIsTheSameTaskAfterARestart(t *testing.T) {
	ctx := context.Background()
	remote := origin(t)
	ws := open(t, remote)
	task, err := ws.Start(ctx, "task-a")
	if err != nil {
		t.Fatal(err)
	}
	sh(t, task.Sandbox(), "echo wip > wip.txt")

	again, err := ws.Resume("task-a")
	if err != nil {
		t.Fatal(err)
	}
	if again.Branch() != task.Branch() || again.Dir() != task.Dir() {
		t.Errorf("resumed %s in %s, want %s in %s", again.Branch(), again.Dir(), task.Branch(), task.Dir())
	}
	if res := sh(t, again.Sandbox(), "cat wip.txt"); res.Stdout != "wip\n" {
		t.Errorf("the resumed task sees %q, want the work in progress", res.Stdout)
	}
	if _, err := ws.Resume("task-b"); err == nil {
		t.Error("resumed a task that was never started")
	}
}

func TestMergedIsOnlyWhatIsOnTheDefaultBranch(t *testing.T) {
	ctx := context.Background()
	remote := origin(t)
	ws := open(t, remote)
	main := git(t, remote, "rev-parse", "main")
	task, _ := ws.Start(ctx, "unmerged")
	sh(t, task.Sandbox(), "git commit -q --allow-empty -m wip")
	task.OpenPR(ctx, "wip", "")
	unmerged := git(t, remote, "rev-parse", task.Branch())

	if got, err := ws.Merged(ctx, main[:10]); err != nil || got != main {
		t.Errorf("Merged(%s) = %q, %v; want the full sha %s", main[:10], got, err, main)
	}
	for _, rev := range []string{unmerged, "0000000deadbeef", "main", "--help"} {
		if got, err := ws.Merged(ctx, rev); err == nil {
			t.Errorf("Merged(%s) = %q, want it refused", rev, got)
		} else if strings.Contains(err.Error(), "hunter2") {
			t.Error(err)
		}
	}
}
