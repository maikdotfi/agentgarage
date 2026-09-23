package model

import (
	"context"

	"charm.land/fantasy"
)

// ToolDefinition is the pure-data tool description sent to the model.
// Mostly added this to avoid `tools` depending directly on `fantasy`
type ToolDefinition struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema
}

// ModelRequest is one stateless completion request.
type ModelRequest struct {
	Model    string
	System   string
	Messages []fantasy.Message
	Tools    []ToolDefinition

	// MaxOutputTokens caps one answer, the model's reasoning included. Zero
	// leaves the provider's own default, which is low — fantasy's Anthropic
	// default is 4096 — and a model that thinks before it answers shares that
	// budget between the thinking and the answer. Such a model can spend the
	// whole of a small budget thinking and return a message with no text in it
	// at all, so a request that asks for something long should say so here.
	MaxOutputTokens int64
}

// ModelClient is the single-completion seam. The fake in tests implements this;
// FantasyModel is the real impl.
type ModelClient interface {
	Generate(ctx context.Context, req ModelRequest) (fantasy.Message, fantasy.Usage, error)
}
