package openai

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// strictCall is what a client that concatenates every fragment by index (as
// openai-python and most SDKs do) reassembles.
type strictCall struct{ id, name, args string }

func drain(t *testing.T, sse string) (calls []strictCall, text string, finish string, err error) {
	t.Helper()
	stream := newTestStream(sse)
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			return calls, text, finish, nil
		}
		if recvErr != nil {
			return calls, text, finish, recvErr
		}
		if event.ContentDelta != nil {
			text += *event.ContentDelta
		}
		if event.FinishReason != nil {
			finish = *event.FinishReason
		}
		if d := event.ToolCallDelta; d != nil {
			if d.Index > len(calls) {
				t.Fatalf("index %d skips ahead of %d calls", d.Index, len(calls))
			}
			if d.Index == len(calls) {
				calls = append(calls, strictCall{})
			}
			c := &calls[d.Index]
			if d.ID != nil {
				c.id += *d.ID
			}
			if d.Name != nil {
				c.name += *d.Name
			}
			if d.ArgumentsDelta != nil {
				c.args += *d.ArgumentsDelta
			}
		}
	}
}

func sse(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\n\n")
	}
	return b.String() + "data: [DONE]\n\n"
}

func wellFormed(t *testing.T, calls []strictCall, names ...string) {
	t.Helper()
	if len(calls) != len(names) {
		t.Fatalf("got %d calls %+v, want %d", len(calls), calls, len(names))
	}
	ids := map[string]bool{}
	for i, c := range calls {
		if c.name != names[i] || c.id == "" || ids[c.id] || !json.Valid([]byte(c.args)) {
			t.Fatalf("call %d malformed: %+v", i, c)
		}
		ids[c.id] = true
	}
}

func TestStream_ToolCallProviderShapes(t *testing.T) {
	// The translating path keeps what providers send; it doesn't repair it.
	for name, tc := range map[string]struct {
		sse    string
		names  []string
		text   string
		finish string
	}{
		"several calls packed in one chunk": {sse: sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"read","arguments":"{\"p\":1}"}},{"index":1,"id":"b","type":"function","function":{"name":"list","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
			names: []string{"read", "list"}, finish: "tool_calls"},
		"several calls packed with the finish reason": {sse: sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{}"}},{"index":1,"id":"b","function":{"name":"read","arguments":"{}"}},{"index":2,"id":"c","function":{"name":"read","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`),
			names: []string{"read", "read", "read"}, finish: "tool_calls"},
		"text in the same chunk as a call": {sse: sse(
			`{"choices":[{"delta":{"content":"Reading.","tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
			names: []string{"read"}, text: "Reading.", finish: "tool_calls"},
		"no finish reason at all": {sse: sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{}"}}]}}]}`),
			names: []string{"read"}, finish: "stop"},
	} {
		t.Run(name, func(t *testing.T) {
			calls, text, finish, err := drain(t, tc.sse)
			if err != nil {
				t.Fatal(err)
			}
			wellFormed(t, calls, tc.names...)
			if text != tc.text || finish != tc.finish {
				t.Fatalf("text %q finish %q", text, finish)
			}
		})
	}
}

func TestStream_UsageBeforeFinishDoesNotEndTheStream(t *testing.T) {
	stream := newTestStream(sse(
		`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`,
		`{"choices":[{"delta":{"content":"late"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
	var got []domain.StreamEvent
	for {
		event, err := stream.Recv()
		if err != nil {
			break
		}
		got = append(got, event)
	}
	if len(got) != 3 || got[0].Type == domain.StreamEventCompleted || got[1].ContentDelta == nil || got[2].Type != domain.StreamEventCompleted || got[2].Usage == nil {
		t.Fatalf("events %+v", got)
	}
}

func TestStream_ProviderFailures(t *testing.T) {
	for name, data := range map[string]string{
		"error payload mid-stream": "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"error\":{\"message\":\"overloaded\",\"type\":\"server_error\"}}\n\n",
		"empty stream":             "data: [DONE]\n\n",
		"nothing at all":           "",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := drain(t, data)
			var gwErr *domain.GatewayError
			if !errors.As(err, &gwErr) {
				t.Fatalf("err = %v, want a provider error", err)
			}
		})
	}
}

func TestStream_LargeSingleLineToolCall(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	calls, _, _, err := drain(t, sse(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"write","arguments":"{\"text\":\"`+big+`\"}"}}]},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	wellFormed(t, calls, "write")
}
