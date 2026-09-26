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

	// MaxOutputTokens caps one answer, the model's reasoning included. Stream
	// allows at least 64000; Generate, which must finish within one idle HTTP
	// response, leaves the provider's default (4096 on Anthropic) unless asked.
	MaxOutputTokens int64
}

// ModelClient is the single-completion seam. The fake in tests implements this;
// FantasyModel is the real impl. Stream is the same completion as it is
// produced; a client with only whole answers can return Streamed(Generate(…)).
type ModelClient interface {
	Generate(ctx context.Context, req ModelRequest) (fantasy.Message, fantasy.Usage, error)
	Stream(ctx context.Context, req ModelRequest) (fantasy.StreamResponse, error)
}
