// Package agents holds the garage's agents. Each one is a chatroom handler:
// mentioned in a room, it does its work and posts back to that room.
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
	"github.com/maikdotfi/agentgarage/metaharness/tools"
	"github.com/maikdotfi/agentgarage/workspace"
)

// DevName is who the dev agent is in the chatroom.
const DevName = "dev"

//go:embed dev.md
var devPrompt string

// DevConfig is what the dev agent needs.
type DevConfig struct {
	Chat       *chatroom.Service
	Model      model.ModelClient
	ModelID    string
	Store      agent.SessionStore // optional; nil keeps sessions in memory only
	Workspaces []*workspace.Workspace
}

// Dev is the dev agent. Each room it is mentioned in becomes one task: a
// worktree and branch in one workspace, and one session that continues across
// mentions in that room.
func Dev(cfg DevConfig) chatroom.Handler {
	d := &dev{cfg: cfg, rooms: map[string]*task{}, bySession: map[string]*task{}}
	opts := []agent.Option{
		agent.WithModel(cfg.Model),
		agent.WithTools(
			agent.Adapt(tools.Bash{}),
			agent.Adapt(tools.ReadFile{}),
			agent.Adapt(tools.EditFile{}),
			agent.Adapt(tools.WriteFile{}),
			d.openPRTool(),
		),
	}
	if cfg.Store != nil {
		opts = append(opts, agent.WithStore(cfg.Store))
	}
	d.agent = agent.New(devPrompt, opts...)
	return d.wake
}

type dev struct {
	cfg   DevConfig
	agent *agent.Agent
	// Mentions are handled one at a time, so these need no lock.
	rooms     map[string]*task
	bySession map[string]*task
}

// task is one room's work.
type task struct {
	room string
	wt   *workspace.Task
	sess *agent.Session
	seen int64 // the last message in the room the agent has read
}

func (d *dev) wake(ctx context.Context, m chatroom.Message) {
	t, ok := d.rooms[m.Room]
	intro := ""
	if !ok {
		ws, question := d.pick(m.Text)
		if ws == nil {
			d.say(ctx, m.Room, question)
			return
		}
		id := slug(m.Room) + "-" + time.Now().UTC().Format("20060102-150405")
		wt, err := ws.Start(ctx, id)
		if err != nil {
			d.say(ctx, m.Room, "I couldn't start a worktree: "+err.Error())
			return
		}
		t = &task{room: m.Room, wt: wt, sess: agent.NewSession(id, d.cfg.ModelID, wt.Sandbox())}
		d.rooms[m.Room], d.bySession[id] = t, t
		intro = fmt.Sprintf("You are working in workspace %s, in a git worktree on branch %s.\n\n", ws.Name(), wt.Branch())
	}

	news, err := d.cfg.Chat.Read(ctx, m.Room, t.seen)
	if err != nil {
		slog.Error("dev: reading room", "room", m.Room, "err", err)
		return
	}
	var others []chatroom.Message
	for _, n := range news {
		t.seen = n.ID
		if n.Author != DevName {
			others = append(others, n)
		}
	}
	if len(others) == 0 {
		return
	}
	prompt := intro + "New messages in room " + m.Room + ":\n\n" + chatroom.Transcript(others)
	t.sess.Messages = append(t.sess.Messages, model.NewUserMessage(prompt))

	events, err := d.agent.Run(ctx, t.sess)
	if err != nil {
		d.say(ctx, m.Room, "I couldn't start: "+err.Error())
		return
	}
	for ev := range events {
		switch ev.Type {
		case agent.EventDone:
			var parts []string
			for _, p := range model.TextParts(ev.Message) {
				parts = append(parts, p.Text)
			}
			if reply := strings.TrimSpace(strings.Join(parts, "\n")); reply != "" {
				d.say(ctx, m.Room, reply)
			}
		case agent.EventError:
			d.say(ctx, m.Room, "I stopped on an error: "+ev.Err.Error())
		}
	}
}

// pick is the workspace a message names, or the only one there is. When it
// can't tell, it returns the question to ask instead.
func (d *dev) pick(text string) (*workspace.Workspace, string) {
	var named, all []string
	var found *workspace.Workspace
	for _, ws := range d.cfg.Workspaces {
		all = append(all, ws.Name())
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(ws.Name()) + `\b`).MatchString(text) {
			named = append(named, ws.Name())
			found = ws
		}
	}
	switch {
	case len(named) == 1:
		return found, ""
	case len(named) == 0 && len(all) == 1:
		return d.cfg.Workspaces[0], ""
	case len(all) == 0:
		return nil, "I have no workspaces to work in."
	}
	return nil, "Which workspace should I work in? I know: " + strings.Join(all, ", ")
}

func (d *dev) say(ctx context.Context, room, text string) {
	if _, err := d.cfg.Chat.Post(ctx, room, DevName, text); err != nil {
		slog.Error("dev: posting", "room", room, "err", err)
	}
}

type openPRArgs struct {
	Title string `json:"title" description:"The PR title: short, imperative."`
	Body  string `json:"body" description:"What changed and why, in Markdown."`
}

func (d *dev) openPRTool() agent.Tool {
	return agent.AdaptFunc(
		agent.ToolMeta{
			Name:        "open_pull_request",
			Description: "Push this task's branch and open a pull request for review. Commit everything first.",
		},
		func(ctx context.Context, ec *agent.ExecCtx, args openPRArgs) (agent.ToolResult, error) {
			t := d.bySession[ec.Session.ID]
			url, err := t.wt.OpenPR(ctx, args.Title, args.Body)
			if err != nil {
				return agent.ToolResult{Content: err.Error(), IsError: true}, nil
			}
			d.say(ctx, t.room, "Opened a PR: "+url)
			return agent.ToolResult{Content: url}, nil
		},
	)
}

var notSlug = regexp.MustCompile(`[^A-Za-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(notSlug.ReplaceAllString(s, "-"), "-")
	if s == "" {
		return "task"
	}
	return strings.ToLower(s)
}
