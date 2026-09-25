package agents_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/maikdotfi/agentgarage/agents"
	"github.com/maikdotfi/agentgarage/metaharness/agent"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/metaharness/testutils"
	"github.com/maikdotfi/agentgarage/workspace"
)

func TestGrugReviewsThePRDevOpensAndDevAnswers(t *testing.T) {
	ws, _, ghLog := newWorkspace(t, "demo")
	chat := newChat(t)
	devModel := &testutils.ScriptedModel{Replies: []model.Message{
		testutils.AssistantToolCall(t, "1", "bash", map[string]string{"cmd": "echo hi > hello.txt && git add hello.txt && git commit -qm 'add hello'"}),
		testutils.AssistantToolCall(t, "2", "open_pull_request", map[string]string{"title": "Add hello", "body": "Says hi."}),
		testutils.AssistantText("Done: https://github.com/example/demo/pull/1"),
		testutils.AssistantText("Thanks. Leaving hello.txt as it is."),
	}}
	grugModel := &testutils.ScriptedModel{Replies: []model.Message{
		testutils.AssistantToolCall(t, "1", "skill", map[string]string{"skill": "grug-review"}),
		testutils.AssistantToolCall(t, "2", "bash", map[string]string{"cmd": "git diff origin/main...HEAD"}),
		testutils.AssistantText("grug see hello.txt. hello simple. grug happy."),
	}}
	workspaces := []*workspace.Workspace{ws}
	chat.Join("dev", agents.Dev(agents.DevConfig{Chat: chat, Model: devModel, ModelID: "x", Store: newStore(t), Workspaces: workspaces}))
	chat.Join("grug", agents.Grug(agents.GrugConfig{Chat: chat, Model: grugModel, ModelID: "x", Store: newStore(t), Workspaces: workspaces}))

	ask := post(t, chat, "hello", "mike", "@dev add hello.txt in demo")

	opened := replyFrom(t, chat, "hello", "dev", ask.ID)
	if !strings.Contains(opened.Text, "https://github.com/example/demo/pull/1") || !strings.Contains(opened.Text, "@grug") {
		t.Errorf("dev's PR message = %q, want the link and @grug", opened.Text)
	}
	review := replyFrom(t, chat, "hello", "grug", opened.ID)
	for _, want := range []string{"@dev", "https://github.com/example/demo/pull/1", "grug happy"} {
		if !strings.Contains(review.Text, want) {
			t.Errorf("grug's room message = %q, want %q in it", review.Text, want)
		}
	}
	answer := replyFrom(t, chat, "hello", "dev", review.ID)
	for answer.Text != "Thanks. Leaving hello.txt as it is." {
		answer = replyFrom(t, chat, "hello", "dev", answer.ID)
	}

	if !strings.Contains(lastToolResult(grugModel.Calls[1]), "grug brained") {
		t.Error("grug did not get the grug-review skill")
	}
	if !strings.Contains(lastToolResult(grugModel.Calls[2]), "+hi") {
		t.Errorf("grug's diff = %q, want the PR's change", lastToolResult(grugModel.Calls[2]))
	}
	gh, _ := os.ReadFile(ghLog)
	if want := "pr\ncomment\nhttps://github.com/example/demo/pull/1\n--body\n"; !strings.Contains(string(gh), want) || !strings.Contains(string(gh), "grug happy") {
		t.Errorf("gh calls do not post the review on the PR:\n%s", gh)
	}
	if !strings.Contains(userText(devModel.Calls[3]), "grug happy") {
		t.Errorf("dev's answer was not prompted with the review:\n%s", userText(devModel.Calls[3]))
	}
}

// openedPR is a PR with one commit in ws, opened without dev.
func openedPR(t *testing.T, ws *workspace.Workspace) *workspace.Task {
	t.Helper()
	task, err := ws.Start(context.Background(), "pr")
	if err != nil {
		t.Fatal(err)
	}
	pushCommit(t, task, "first change")
	return task
}

func pushCommit(t *testing.T, task *workspace.Task, msg string) {
	t.Helper()
	res, err := task.Sandbox().Exec(context.Background(), agent.Command{Cmd: "sh", Args: []string{"-c", "git commit -q --allow-empty -m '" + msg + "'"}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("commit: %v %s", err, res.Stderr)
	}
	if _, err := task.OpenPR(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
}

func TestGrugReviewsEachPRHeadOnce(t *testing.T) {
	ws, _, ghLog := newWorkspace(t, "demo")
	chat := newChat(t)
	m := &testutils.ScriptedModel{Replies: []model.Message{
		testutils.AssistantText("review one"),
		testutils.AssistantText("review two"),
	}}
	chat.Join("grug", agents.Grug(agents.GrugConfig{Chat: chat, Model: m, ModelID: "x", Store: newStore(t), Workspaces: []*workspace.Workspace{ws}}))
	task := openedPR(t, ws)
	const ask = "@grug review https://github.com/example/demo/pull/1"

	first := replyFrom(t, chat, "r", "grug", post(t, chat, "r", "mike", ask).ID)
	if !strings.Contains(first.Text, "@mike") || !strings.Contains(first.Text, "review one") {
		t.Errorf("first review = %q, want it to mention who asked", first.Text)
	}

	again := replyFrom(t, chat, "r", "grug", post(t, chat, "r", "dev", ask+" again").ID)
	if strings.Contains(again.Text, "@") {
		t.Errorf("grug's answer to a repeat ask = %q; it mentions someone, which could start a loop", again.Text)
	}
	if len(m.Calls) != 1 {
		t.Errorf("grug ran the model %d times for one head, want once", len(m.Calls))
	}

	pushCommit(t, task, "answer the review")
	second := replyFrom(t, chat, "r", "grug", post(t, chat, "r", "dev", ask).ID)
	if !strings.Contains(second.Text, "review two") {
		t.Errorf("after a new commit grug said %q, want a new review", second.Text)
	}
	gh, _ := os.ReadFile(ghLog)
	if n := strings.Count(string(gh), "pr\ncomment\n"); n != 2 {
		t.Errorf("grug commented %d times on the PR, want 2", n)
	}
}

func TestGrugStopsReviewingAPRAfterThreeRounds(t *testing.T) {
	ws, _, _ := newWorkspace(t, "demo")
	chat := newChat(t)
	m := &testutils.ScriptedModel{Replies: []model.Message{
		testutils.AssistantText("round 1"), testutils.AssistantText("round 2"), testutils.AssistantText("round 3"),
	}}
	chat.Join("grug", agents.Grug(agents.GrugConfig{Chat: chat, Model: m, ModelID: "x", Store: newStore(t), Workspaces: []*workspace.Workspace{ws}}))
	task := openedPR(t, ws)

	var reply string
	for round := range 4 {
		if round > 0 {
			pushCommit(t, task, "more")
		}
		reply = replyFrom(t, chat, "r", "grug", post(t, chat, "r", "dev", "@grug https://github.com/example/demo/pull/1").ID).Text
	}
	if len(m.Calls) != 3 {
		t.Errorf("grug reviewed %d times, want 3", len(m.Calls))
	}
	if strings.Contains(reply, "@") || !strings.Contains(reply, "human") {
		t.Errorf("fourth answer = %q, want it to hand over to a human without mentioning anyone", reply)
	}
}

func TestGrugWantsAPRLink(t *testing.T) {
	ws, _, _ := newWorkspace(t, "demo")
	chat := newChat(t)
	m := &testutils.ScriptedModel{}
	chat.Join("grug", agents.Grug(agents.GrugConfig{Chat: chat, Model: m, ModelID: "x", Store: newStore(t), Workspaces: []*workspace.Workspace{ws}}))

	reply := replyFrom(t, chat, "r", "grug", post(t, chat, "r", "dev", "thanks @grug!").ID)
	if strings.Contains(reply.Text, "@") || !strings.Contains(reply.Text, "link") {
		t.Errorf("reply = %q, want a plain ask for a PR link", reply.Text)
	}
	if len(m.Calls) != 0 {
		t.Errorf("the model ran %d times without a PR", len(m.Calls))
	}
}
