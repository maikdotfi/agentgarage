package ui

import (
	"strings"

	"charm.land/fantasy"

	"github.com/maikdotfi/agentgarage/metaharness/agent"
	"github.com/maikdotfi/agentgarage/metaharness/model"
)

// sessionData is what the session page renders.
type sessionData struct {
	Agent   string
	Room    string // the room this session worked for, when there is one
	Session *agent.Session
	Turns   []turn
	// NoAnswer flags a session whose last assistant message had no text:
	// the turn that ended silently.
	NoAnswer bool
}

// turn is one message of the transcript as the page shows it.
type turn struct {
	Role      string
	Text      string
	Reasoning string   // folded away, shown on demand
	Calls     []call   // assistant: the tools it asked for
	Results   []result // tool: what came back, errors marked
}

// call is one tool invocation with its input.
type call struct {
	Name  string
	Input string
}

// result is one tool's output.
type result struct {
	Text  string
	Error bool
}

// turns flattens a transcript into what the page renders. Empty turns (a tool
// message whose parts are all it holds, an assistant message with nothing in
// it) are dropped; the page shows what happened, not the wire format.
func turns(sess *agent.Session) []turn {
	var ts []turn
	for _, m := range sess.Messages {
		t := turn{Role: string(m.Role)}
		switch m.Role {
		case fantasy.MessageRoleUser:
			for _, p := range model.TextParts(&m) {
				t.Text += p.Text
			}
		case fantasy.MessageRoleAssistant:
			for _, p := range model.ReasoningParts(&m) {
				t.Reasoning += p.Text
			}
			for _, p := range model.TextParts(&m) {
				t.Text += p.Text
			}
			for _, p := range model.ToolCalls(&m) {
				t.Calls = append(t.Calls, call{Name: p.Name, Input: p.Input})
			}
		case fantasy.MessageRoleTool:
			for _, p := range model.ToolResults(&m) {
				r := result{Text: p.Text, Error: p.Error != nil}
				if p.Error != nil {
					r.Text = p.Error.Error()
				}
				t.Results = append(t.Results, r)
			}
		}
		if t.Text != "" || t.Reasoning != "" || len(t.Calls) > 0 || len(t.Results) > 0 {
			ts = append(ts, t)
		}
	}
	return ts
}

// noAnswer is a session whose last assistant message has no text: a turn that
// ended without an answer, the thing the pages exist to make obvious.
func noAnswer(sess *agent.Session) bool {
	var last *fantasy.Message
	for i := range sess.Messages {
		if sess.Messages[i].Role == fantasy.MessageRoleAssistant {
			last = &sess.Messages[i]
		}
	}
	if last == nil {
		return false
	}
	var text string
	for _, p := range model.TextParts(last) {
		text += p.Text
	}
	return strings.TrimSpace(text) == ""
}
