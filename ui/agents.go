package ui

import (
	"github.com/maikdotfi/agentgarage/metaharness/agent"
	"github.com/maikdotfi/agentgarage/metaharness/agentdb"
)

// AgentStore is the read-only view of one agent's database that the pages
// observe: its sessions, and the keys the garage keeps next to them. It is
// what a store already is (a turso.Store is one), so serve hands the store it
// opened; the pages never write through it.
type AgentStore interface {
	agent.SessionStore
	agent.SessionLister
	agentdb.KV
}

// Agent is one agent as the pages see it: who it is, the model serve logged
// it as running, its own database read-only, and — when the caller tracks
// turns — where it is working now (see Busy).
type Agent struct {
	Name  string
	Model string
	Store AgentStore
	Busy  *Busy // may be nil: this agent never says where it is
}
