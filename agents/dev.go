// Package agents holds the garage's agents. Each one is a chatroom handler:
// mentioned in a room, it does its work and posts back to that room.
package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/maikdotfi/agentgarage/chatroom"
	"github.com/maikdotfi/agentgarage/metaharness/agent"
	"github.com/maikdotfi/agentgarage/metaharness/agentdb"
	"github.com/maikdotfi/agentgarage/metaharness/model"
	"github.com/maikdotfi/agentgarage/metaharness/tools"
	"github.com/maikdotfi/agentgarage/workspace"
)

// DevName is who the dev agent is in the chatroom.
const DevName = "dev"

// GarageName is who the garage's own notices are by, such as the "running
// <sha>" it posts when it boots into a release.
const GarageName = "garage"

//go:embed dev.md
var devPrompt string

// Store is an agent's own database: its sessions, and what it keeps between
// them. A turso.Store is one.
type Store interface {
	agent.SessionStore
	agentdb.KV
}

// DevConfig is what the dev agent needs.
type DevConfig struct {
	Chat       *chatroom.Service
	Model      model.ModelClient
	ModelID    string
	Store      Store // sessions, and which room is which task
	Workspaces []*workspace.Workspace
	// Deploy releases the garage at sha, a commit on main, on behalf of room.
	// Nil means dev has no deploy tool.
	Deploy func(ctx context.Context, room, sha string) (string, error)
}

// Dev is the dev agent. Each room it is mentioned in becomes one task: a
// worktree and branch in one workspace, and one session that continues across
// mentions in that room.
func Dev(cfg DevConfig) chatroom.Handler {
	d := &dev{cfg: cfg, rooms: map[string]*task{}, bySession: map[string]*task{}}
	tools := []agent.Tool{
		agent.Adapt(tools.Bash{}),
		agent.Adapt(tools.ReadFile{}),
		agent.Adapt(tools.EditFile{}),
		agent.Adapt(tools.WriteFile{}),
		d.openPRTool(),
	}
	if cfg.Deploy != nil {
		tools = append(tools, d.deployTool())
	}
	d.agent = agent.New(devPrompt, agent.WithModel(cfg.Model), agent.WithStore(cfg.Store), agent.WithTools(tools...))
	return d.wake
}

type dev struct {
	cfg   DevConfig
	agent *agent.Agent
	// The tasks running in this process. Mentions are handled one at a time,
	// so these need no lock.
	rooms     map[string]*task
	bySession map[string]*task
}

// task is one room's work.
type task struct {
	room string
	ws   *workspace.Workspace
	wt   *workspace.Task
	sess *agent.Session
	seen int64  // the last message in the room the agent has read
	by   string // who woke the turn running now
}

// taskRecord is a room's task as dev's database keeps it, under roomKey. The
// session's ID is the task's.
type taskRecord struct {
	Workspace string `json:"workspace"`
	Task      string `json:"task"`
	Seen      int64  `json:"seen"`
}

func roomKey(room string) string { return "dev/rooms/" + room }

func (d *dev) wake(ctx context.Context, m chatroom.Message) {
	t, intro := d.task(ctx, m)
	if t == nil {
		return
	}
	t.by = m.Author

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
	d.remember(ctx, t)
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

// task is the room's task: the one running here, the one dev's database
// remembers from before a restart, or a new one. A new task comes with the
// intro for its first prompt; nil means dev has answered already.
func (d *dev) task(ctx context.Context, m chatroom.Message) (*task, string) {
	if t, ok := d.rooms[m.Room]; ok {
		return t, ""
	}
	if t, err := d.resume(ctx, m.Room); err != nil {
		d.say(ctx, m.Room, "I lost this room's task ("+err.Error()+"), so I'm starting a new one.")
	} else if t != nil {
		d.rooms[m.Room], d.bySession[t.sess.ID] = t, t
		return t, ""
	}

	ws, question := pick(d.cfg.Workspaces, m.Text)
	if ws == nil {
		d.say(ctx, m.Room, question)
		return nil, ""
	}
	id := slug(m.Room) + "-" + time.Now().UTC().Format("20060102-150405")
	wt, err := ws.Start(ctx, id)
	if err != nil {
		d.say(ctx, m.Room, "I couldn't start a worktree: "+err.Error())
		return nil, ""
	}
	t := &task{room: m.Room, ws: ws, wt: wt, sess: agent.NewSession(id, d.cfg.ModelID, wt.Sandbox())}
	d.rooms[m.Room], d.bySession[id] = t, t
	return t, fmt.Sprintf("You are working in workspace %s, in a git worktree on branch %s.\n\n", ws.Name(), wt.Branch())
}

// resume is the room's task as the database remembers it, back in its
// worktree, or nil if the room has none.
func (d *dev) resume(ctx context.Context, room string) (*task, error) {
	raw, found, err := d.cfg.Store.Get(ctx, roomKey(room))
	if err != nil || !found {
		return nil, err
	}
	var rec taskRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	var ws *workspace.Workspace
	for _, w := range d.cfg.Workspaces {
		if w.Name() == rec.Workspace {
			ws = w
		}
	}
	if ws == nil {
		return nil, fmt.Errorf("no workspace %s", rec.Workspace)
	}
	wt, err := ws.Resume(rec.Task)
	if err != nil {
		return nil, err
	}
	sess, err := d.cfg.Store.Load(ctx, rec.Task)
	if err != nil {
		return nil, err
	}
	if err := sess.Bind(wt.Sandbox()); err != nil {
		return nil, err
	}
	return &task{room: room, ws: ws, wt: wt, sess: sess, seen: rec.Seen}, nil
}

// remember writes the room's task to dev's database.
func (d *dev) remember(ctx context.Context, t *task) {
	raw, _ := json.Marshal(taskRecord{Workspace: t.ws.Name(), Task: t.sess.ID, Seen: t.seen})
	if err := d.cfg.Store.Put(ctx, roomKey(t.room), raw); err != nil {
		slog.Error("dev: remembering the task", "room", t.room, "err", err)
	}
}

// pick is the workspace a message names, or the only one there is. When it
// can't tell, it returns the question to ask instead.
func pick(workspaces []*workspace.Workspace, text string) (*workspace.Workspace, string) {
	var named, all []string
	var found *workspace.Workspace
	for _, ws := range workspaces {
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
		return workspaces[0], ""
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
			Description: "Push this task's branch and open a pull request for review, or update the one already open. Either way grug is asked to review it. Commit everything first.",
		},
		func(ctx context.Context, ec *agent.ExecCtx, args openPRArgs) (agent.ToolResult, error) {
			t := d.bySession[ec.Session.ID]
			url, err := t.wt.OpenPR(ctx, args.Title, args.Body)
			if err != nil {
				return agent.ToolResult{Content: err.Error(), IsError: true}, nil
			}
			d.say(ctx, t.room, "PR for review: "+url+"\n\n@"+GrugName+", please review it.")
			return agent.ToolResult{Content: url}, nil
		},
	)
}

type deployArgs struct {
	SHA string `json:"sha" description:"The commit to deploy, as a sha. It must be on main."`
}

func (d *dev) deployTool() agent.Tool {
	return agent.AdaptFunc(
		agent.ToolMeta{
			Name:        "deploy",
			Description: "Deploy the garage you run in at a commit on main, when a human asks you to. The garage tests and builds that commit itself, releases it and restarts into it, which ends this turn; when it is back it posts \"running <sha>\" in this room.",
		},
		func(ctx context.Context, ec *agent.ExecCtx, args deployArgs) (agent.ToolResult, error) {
			t := d.bySession[ec.Session.ID]
			if !human(t.by) {
				return agent.ToolResult{Content: "Only a human can ask for a deploy, and " + t.by + " woke you. Ask in the room and wait for one.", IsError: true}, nil
			}
			out, err := d.cfg.Deploy(ctx, t.room, args.SHA)
			if err != nil {
				return agent.ToolResult{Content: err.Error(), IsError: true}, nil
			}
			return agent.ToolResult{Content: out}, nil
		},
	)
}

// human is whether a message's author is a human: not an agent here, nor the
// garage. Only a human's ask may deploy, so no notice can start another.
func human(author string) bool {
	return author != DevName && author != GrugName && author != GarageName
}

var notSlug = regexp.MustCompile(`[^A-Za-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(notSlug.ReplaceAllString(s, "-"), "-")
	if s == "" {
		return "task"
	}
	return strings.ToLower(s)
}
