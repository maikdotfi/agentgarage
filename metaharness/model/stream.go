package model

import (
	"errors"
	"strconv"
	"strings"

	"charm.land/fantasy"
)

// ErrOutputLimit is an answer that ran out of output tokens before the model
// wrote any text or asked for a tool: usually it was still thinking.
var ErrOutputLimit = errors.New("model: ran out of output tokens before answering")

// streamMinOutputTokens is the least output-token allowance a streamed call
// gets. Reasoning shares it with the answer, and some models reason unasked.
const streamMinOutputTokens int64 = 64000

// Collect reads a streamed answer to its end and returns it as one assistant
// message: reasoning first, then text, then tool calls, as Generate builds it.
func Collect(parts fantasy.StreamResponse) (fantasy.Message, fantasy.Usage, error) {
	var (
		reasoning []fantasy.ReasoningPart
		thinking  = map[string]int{} // reasoning block id -> index in reasoning
		text      strings.Builder
		calls     []fantasy.MessagePart
		usage     fantasy.Usage
		finish    fantasy.FinishReason
	)
	for p := range parts {
		switch p.Type {
		case fantasy.StreamPartTypeReasoningStart:
			thinking[p.ID] = len(reasoning)
			reasoning = append(reasoning, fantasy.ReasoningPart{ProviderOptions: fantasy.ProviderOptions(p.ProviderMetadata)})
		case fantasy.StreamPartTypeReasoningDelta, fantasy.StreamPartTypeReasoningEnd:
			if i, ok := thinking[p.ID]; ok {
				reasoning[i].Text += p.Delta
				if len(p.ProviderMetadata) > 0 {
					reasoning[i].ProviderOptions = fantasy.ProviderOptions(p.ProviderMetadata)
				}
			}
		case fantasy.StreamPartTypeTextDelta:
			text.WriteString(p.Delta)
		case fantasy.StreamPartTypeToolCall:
			if !p.ProviderExecuted {
				calls = append(calls, fantasy.ToolCallPart{ToolCallID: p.ID, ToolName: p.ToolCallName, Input: p.ToolCallInput})
			}
		case fantasy.StreamPartTypeFinish:
			usage, finish = p.Usage, p.FinishReason
		case fantasy.StreamPartTypeError:
			return fantasy.Message{}, usage, p.Error
		}
	}
	if finish == fantasy.FinishReasonLength && text.Len() == 0 && len(calls) == 0 {
		return fantasy.Message{}, usage, ErrOutputLimit
	}

	msg := fantasy.Message{Role: fantasy.MessageRoleAssistant}
	for _, r := range reasoning {
		msg.Content = append(msg.Content, r)
	}
	if text.Len() > 0 {
		msg.Content = append(msg.Content, fantasy.TextPart{Text: text.String()})
	}
	msg.Content = append(msg.Content, calls...)
	return msg, usage, nil
}

// Streamed is a whole answer as a stream, for a client that has only whole
// answers: Stream can return Streamed(m.Generate(ctx, req)).
func Streamed(msg fantasy.Message, usage fantasy.Usage, err error) (fantasy.StreamResponse, error) {
	if err != nil {
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		for i, part := range msg.Content {
			var p []fantasy.StreamPart
			if r, ok := fantasy.AsMessagePart[fantasy.ReasoningPart](part); ok {
				id := strconv.Itoa(i)
				p = []fantasy.StreamPart{
					{Type: fantasy.StreamPartTypeReasoningStart, ID: id},
					{Type: fantasy.StreamPartTypeReasoningDelta, ID: id, Delta: r.Text},
					{Type: fantasy.StreamPartTypeReasoningEnd, ID: id, ProviderMetadata: fantasy.ProviderMetadata(r.ProviderOptions)},
				}
			} else if t, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				p = []fantasy.StreamPart{{Type: fantasy.StreamPartTypeTextDelta, Delta: t.Text}}
			} else if c, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part); ok {
				p = []fantasy.StreamPart{{Type: fantasy.StreamPartTypeToolCall, ID: c.ToolCallID, ToolCallName: c.ToolName, ToolCallInput: c.Input}}
			}
			for _, sp := range p {
				if !yield(sp) {
					return
				}
			}
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, Usage: usage, FinishReason: fantasy.FinishReasonStop})
	}, nil
}
