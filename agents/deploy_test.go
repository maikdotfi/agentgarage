package agents_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/maikdotfi/agentgarage/agents"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/metaharness/testutils"
	"github.com/maikdotfi/agentgarage/workspace"
)

func TestDevDeploysWhenAHumanAsksButNotOnTheBootNotice(t *testing.T) {
	ws, _, _ := newWorkspace(t, "demo")
	chat := newChat(t)
	m := &testutils.ScriptedModel{Replies: []model.Message{
		testutils.AssistantToolCall(t, "1", "deploy", map[string]string{"sha": "abc1234"}),
		testutils.AssistantText("deploying abc1234"),
		testutils.AssistantToolCall(t, "2", "deploy", map[string]string{"sha": "abc1234"}),
		testutils.AssistantText("checked"),
	}}
	type call struct{ room, sha string }
	var deploys []call
	deploy := func(_ context.Context, room, sha string) (string, error) {
		deploys = append(deploys, call{room, sha})
		return "released " + sha, nil
	}
	chat.Join("dev", agents.Dev(agents.DevConfig{Chat: chat, Model: m, ModelID: "x", Store: newStore(t),
		Workspaces: []*workspace.Workspace{ws}, Deploy: deploy}))

	ask := post(t, chat, "ship", "mike", "@dev deploy abc1234")
	replyFrom(t, chat, "ship", "dev", ask.ID)
	if len(deploys) != 1 || deploys[0] != (call{"ship", "abc1234"}) {
		t.Fatalf("deploys = %v, want abc1234 from room ship", deploys)
	}
	if got := lastToolResult(m.Calls[1]); !strings.Contains(got, "released abc1234") {
		t.Errorf("deploy told the model %q", got)
	}

	boot := post(t, chat, "ship", agents.GarageName, "@dev running abc1234")
	replyFrom(t, chat, "ship", "dev", boot.ID)
	if len(deploys) != 1 {
		t.Errorf("the boot notice led to another deploy: %v", deploys)
	}
	if got := fmt.Sprint(m.Calls[3].Messages[len(m.Calls[3].Messages)-1]); !strings.Contains(got, "Only a human") {
		t.Errorf("a deploy on the boot notice told the model %q, want it refused until a human asks", got)
	}
}

func TestDevHasNoDeployWhereTheGarageCannotDeploy(t *testing.T) {
	ws, _, _ := newWorkspace(t, "demo")
	chat := newChat(t)
	m := &testutils.ScriptedModel{Replies: []model.Message{testutils.AssistantText("ok")}}
	chat.Join("dev", agents.Dev(agents.DevConfig{Chat: chat, Model: m, ModelID: "x", Store: newStore(t), Workspaces: []*workspace.Workspace{ws}}))

	replyFrom(t, chat, "r", "dev", post(t, chat, "r", "mike", "@dev hi").ID)
	for _, tool := range m.Calls[0].Tools {
		if tool.Name == "deploy" {
			t.Error("dev was offered deploy with nothing to deploy through")
		}
	}
}
