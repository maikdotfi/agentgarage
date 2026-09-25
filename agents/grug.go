package agents

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/metaharness/agent"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/metaharness/skills"
	"github.com/maikdotfi/agentgarage/metaharness/tools"
	"github.com/maikdotfi/agentgarage/workspace"
)

// GrugName is who the reviewer is in the chatroom.
const GrugName = "grug"

// maxReviews is how many heads of one PR grug reviews before a human takes over.
const maxReviews = 3

//go:embed grug.md
var grugPrompt string

// GrugConfig is what grug needs.
type GrugConfig struct {
	Chat       *chatroom.Service
	Model      model.ModelClient
	ModelID    string
	Workspaces []*workspace.Workspace
}

// Grug is the reviewer. Mentioned with a PR link, it reviews the PR's head in
// a worktree of its own, posts the review on the PR, and answers whoever
// asked in the room. Each head is reviewed once, and a PR at most maxReviews
// times, so grug and dev can't keep waking each other.
func Grug(cfg GrugConfig) chatroom.Handler {
	g := &grug{cfg: cfg, reviewed: map[string][]string{}}
	g.agent = agent.New(grugPrompt,
		agent.WithModel(cfg.Model),
		agent.WithTools(
			agent.Adapt(tools.Bash{}),
			agent.Adapt(tools.ReadFile{}),
			tools.NewSkill(skills.GrugReview()),
		),
	)
	return g.wake
}

type grug struct {
	cfg   GrugConfig
	agent *agent.Agent
	// reviewed is the heads reviewed per PR URL. Mentions are handled one at
	// a time, so it needs no lock.
	reviewed map[string][]string
}

var prLink = regexp.MustCompile(`https?://\S+/pull/\d+`)

// wake reviews the PR m links to. Only a review mentions anyone; every other
// answer is plain, so it can't wake another agent.
func (g *grug) wake(ctx context.Context, m chatroom.Message) {
	url := prLink.FindString(m.Text)
	if url == "" {
		g.say(ctx, m.Room, "grug review pull requests. give grug a PR link.")
		return
	}
	ws, question := pick(g.cfg.Workspaces, m.Text)
	if ws == nil {
		g.say(ctx, m.Room, question)
		return
	}
	pr, err := ws.PullRequest(ctx, url)
	if err != nil {
		g.say(ctx, m.Room, "grug can't see "+url+": "+err.Error())
		return
	}
	heads := g.reviewed[pr.URL]
	switch {
	case len(heads) > 0 && heads[len(heads)-1] == pr.HeadSHA:
		g.say(ctx, m.Room, fmt.Sprintf("grug already reviewed %s at %s. push new commits, then ask again.", pr.URL, short(pr.HeadSHA)))
		return
	case len(heads) >= maxReviews:
		g.say(ctx, m.Room, fmt.Sprintf("grug reviewed %s %d times. a human takes it from here.", pr.URL, len(heads)))
		return
	}

	review, err := g.review(ctx, ws, pr, m)
	if err != nil {
		g.say(ctx, m.Room, "grug stopped reviewing "+pr.URL+": "+err.Error())
		return
	}
	g.reviewed[pr.URL] = append(heads, pr.HeadSHA)
	if err := ws.Comment(ctx, pr.URL, fmt.Sprintf("grug review of %s:\n\n%s", short(pr.HeadSHA), review)); err != nil {
		g.say(ctx, m.Room, "grug couldn't post on "+pr.URL+": "+err.Error())
	}
	g.say(ctx, m.Room, fmt.Sprintf("@%s grug reviewed %s at %s (also on the PR):\n\n%s", m.Author, pr.URL, short(pr.HeadSHA), review))
}

// review runs one fresh session over a checkout of the PR's head and returns
// the final answer. The checkout is removed afterwards.
func (g *grug) review(ctx context.Context, ws *workspace.Workspace, pr workspace.PullRequest, m chatroom.Message) (string, error) {
	id := "review-" + short(pr.HeadSHA) + "-" + time.Now().UTC().Format("20060102-150405")
	wt, err := ws.Checkout(ctx, id, pr)
	if err != nil {
		return "", err
	}
	defer wt.Close()
	sess := agent.NewSession(id, g.cfg.ModelID, wt.Sandbox())
	sess.Messages = append(sess.Messages, model.NewUserMessage(fmt.Sprintf(
		"Review pull request %s: branch %s at %s, into %s. Your worktree is at its head; the change is `git diff origin/%s...HEAD`.\n\nAsked in room %s:\n\n%s",
		pr.URL, pr.Head, pr.HeadSHA, pr.Base, pr.Base, m.Room, chatroom.Transcript([]chatroom.Message{m}))))

	events, err := g.agent.Run(ctx, sess)
	if err != nil {
		return "", err
	}
	var review string
	for ev := range events {
		switch ev.Type {
		case agent.EventDone:
			var parts []string
			for _, p := range model.TextParts(ev.Message) {
				parts = append(parts, p.Text)
			}
			review = strings.TrimSpace(strings.Join(parts, "\n"))
		case agent.EventError:
			err = ev.Err
		}
	}
	if err == nil && review == "" {
		err = fmt.Errorf("the review came back empty")
	}
	return review, err
}

func (g *grug) say(ctx context.Context, room, text string) {
	if _, err := g.cfg.Chat.Post(ctx, room, GrugName, text); err != nil {
		slog.Error("grug: posting", "room", room, "err", err)
	}
}

func short(sha string) string { return sha[:min(len(sha), 7)] }
