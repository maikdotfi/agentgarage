package model_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/maikdotfi/agentgarage/metaharness/model"
)

// block is one content block of a streamed Anthropic answer: "thinking" and
// "text" carry text, "tool_use" carries a tool name and its JSON input.
type block struct{ kind, text, tool string }

// anthropicStream is a Messages endpoint that streams blocks, then stops for
// stop, and reports the max_tokens each request asked for.
func anthropicStream(t *testing.T, stop string, blocks ...block) (model.ModelClient, <-chan int64) {
	t.Helper()
	asked := make(chan int64, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			MaxTokens int64 `json:"max_tokens"`
			Stream    bool  `json:"stream"`
		}
		if err := json.Unmarshal(body, &req); err != nil || !req.Stream {
			t.Errorf("want a streaming request, got %s", body)
		}
		asked <- req.MaxTokens
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(event, data string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data) }
		send("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":7,"output_tokens":0}}}`)
		for i, b := range blocks {
			q, _ := json.Marshal(b.text)
			switch b.kind {
			case "thinking":
				send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":"","signature":""}}`, i))
				send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":%s}}`, i, q))
			case "text":
				send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, i))
				send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, i, q))
			case "tool_use":
				send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"call_%d","name":%q,"input":{}}}`, i, i, b.tool))
				send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`, i, q))
			}
			send("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i))
		}
		send("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":42}}`, stop))
		send("message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(srv.Close)
	m, err := model.New(model.Config{Provider: model.ProviderAnthropic, APIKey: "k", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return m, asked
}

func streamed(t *testing.T, m model.ModelClient, req model.ModelRequest) (fantasy.Message, fantasy.Usage, error) {
	t.Helper()
	req.Model = "m"
	req.Messages = []fantasy.Message{model.NewUserMessage("hi")}
	parts, err := m.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return model.Collect(parts)
}

func TestStreamCollectsTheAnswer(t *testing.T) {
	m, _ := anthropicStream(t, "tool_use",
		block{kind: "thinking", text: "first ls"},
		block{kind: "text", text: "Looking."},
		block{kind: "tool_use", tool: "bash", text: `{"cmd":"ls"}`},
	)
	msg, usage, err := streamed(t, m, model.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if r := model.ReasoningParts(&msg); len(r) != 1 || r[0].Text != "first ls" {
		t.Errorf("reasoning = %v, want %q", r, "first ls")
	}
	if tx := model.TextParts(&msg); len(tx) != 1 || tx[0].Text != "Looking." {
		t.Errorf("text = %v, want %q", tx, "Looking.")
	}
	if c := model.ToolCalls(&msg); len(c) != 1 || c[0].Name != "bash" || c[0].Input != `{"cmd":"ls"}` {
		t.Errorf("tool calls = %+v, want bash {\"cmd\":\"ls\"}", c)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 42 {
		t.Errorf("usage = %+v, want 7 in, 42 out", usage)
	}
}

// A model that thinks, asked or not, needs room to think in; streaming is what
// lets a call ask for that much without timing out.
func TestStreamLeavesRoomToThink(t *testing.T) {
	m, asked := anthropicStream(t, "end_turn", block{kind: "text", text: "done"})
	if _, _, err := streamed(t, m, model.ModelRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := <-asked; got < 64000 {
		t.Errorf("max_tokens = %d, want at least 64000", got)
	}
}

func TestStreamAsksForMoreWhenTheRequestDoes(t *testing.T) {
	m, asked := anthropicStream(t, "end_turn", block{kind: "text", text: "done"})
	if _, _, err := streamed(t, m, model.ModelRequest{MaxOutputTokens: 100000}); err != nil {
		t.Fatal(err)
	}
	if got := <-asked; got != 100000 {
		t.Errorf("max_tokens = %d, want 100000", got)
	}
}

// A call cut off while the model was still thinking has nothing to act on, and
// must not look like a finished answer.
func TestStreamCutOffBeforeAnyAnswerIsAnError(t *testing.T) {
	m, _ := anthropicStream(t, "max_tokens", block{kind: "thinking", text: "let me think about the design"})
	if _, _, err := streamed(t, m, model.ModelRequest{}); !errors.Is(err, model.ErrOutputLimit) {
		t.Fatalf("err = %v, want ErrOutputLimit", err)
	}
}

func TestStreamCutOffAfterSomeTextIsTheText(t *testing.T) {
	m, _ := anthropicStream(t, "max_tokens", block{kind: "text", text: "half an answ"})
	msg, _, err := streamed(t, m, model.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if tx := model.TextParts(&msg); len(tx) != 1 || tx[0].Text != "half an answ" {
		t.Errorf("text = %v, want the partial answer", tx)
	}
}

// Streamed lets a client that only has whole answers, such as a test fake, be
// streamed like any other.
func TestStreamedIsCollectedBackUnchanged(t *testing.T) {
	want := fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
		fantasy.ReasoningPart{Text: "hm"},
		fantasy.TextPart{Text: "hello"},
		fantasy.ToolCallPart{ToolCallID: "c1", ToolName: "bash", Input: `{"cmd":"ls"}`},
	}}
	parts, err := model.Streamed(want, fantasy.Usage{InputTokens: 3, OutputTokens: 4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, usage, err := model.Collect(parts)
	if err != nil {
		t.Fatal(err)
	}
	if g, w := fmt.Sprint(got), fmt.Sprint(want); g != w {
		t.Errorf("message = %s, want %s", g, w)
	}
	if usage.InputTokens != 3 || usage.OutputTokens != 4 {
		t.Errorf("usage = %+v, want 3 in, 4 out", usage)
	}

	boom := errors.New("boom")
	if _, err := model.Streamed(fantasy.Message{}, fantasy.Usage{}, boom); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestCollectReportsAStreamError(t *testing.T) {
	boom := errors.New("connection reset")
	parts := func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "0", Delta: "par"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: boom})
	}
	if _, _, err := model.Collect(parts); !errors.Is(err, boom) || strings.Contains(fmt.Sprint(err), "par") {
		t.Fatalf("err = %v, want connection reset", err)
	}
}
