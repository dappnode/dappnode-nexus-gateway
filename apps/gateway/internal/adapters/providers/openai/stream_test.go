package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type fakeBody struct {
	*strings.Reader
}

func (fakeBody) Close() error { return nil }

func newTestStream(sseData string) *Stream {
	body := fakeBody{strings.NewReader(sseData)}
	resp := &http.Response{Body: body}
	return NewStream(resp)
}

func TestStream_ToolCallDeltaAndFinishReasonInSameChunk(t *testing.T) {
	sseData := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"path\\\":\\\"f.txt\\\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n"

	body := fakeBody{strings.NewReader(sseData)}
	stream := NewStream(&http.Response{Body: body}, "deepseek")

	ev1, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev1: %v", err)
	}
	if ev1.Type != domain.StreamEventOutputMessageDelta {
		t.Fatalf("ev1 type = %v, want OutputMessageDelta", ev1.Type)
	}

	ev2, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev2: %v", err)
	}
	if ev2.Type != domain.StreamEventToolCallDelta {
		t.Fatalf("ev2 type = %v, want ToolCallDelta", ev2.Type)
	}

	ev3, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev3: %v", err)
	}
	if ev3.Type != domain.StreamEventToolCallDelta {
		t.Fatalf("ev3 type = %v, want ToolCallDelta", ev3.Type)
	}

	// The combined chunk with tool_calls + finish_reason must emit
	// the tool call delta FIRST, then the completed event.
	ev4, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev4: %v", err)
	}
	if ev4.Type != domain.StreamEventToolCallDelta {
		t.Fatalf("ev4 type = %v, want ToolCallDelta (deferred finish)", ev4.Type)
	}
	if ev4.ToolCallDelta == nil || ev4.ToolCallDelta.ArgumentsDelta == nil {
		t.Fatal("ev4: missing arguments delta")
	}
	if *ev4.ToolCallDelta.ArgumentsDelta != "}" {
		t.Fatalf("ev4 args = %q, want %q", *ev4.ToolCallDelta.ArgumentsDelta, "}")
	}

	ev5, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev5: %v", err)
	}
	if ev5.Type != domain.StreamEventCompleted {
		t.Fatalf("ev5 type = %v, want Completed", ev5.Type)
	}
	if ev5.FinishReason == nil || *ev5.FinishReason != "tool_calls" {
		t.Fatalf("ev5 finish_reason = %v, want tool_calls", ev5.FinishReason)
	}

	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestStream_FinishReasonWithoutToolCalls(t *testing.T) {
	sseData := "data: {\"id\":\"c2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n"

	body := fakeBody{strings.NewReader(sseData)}
	stream := NewStream(&http.Response{Body: body}, "deepseek")

	ev1, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev1: %v", err)
	}
	if ev1.Type != domain.StreamEventOutputTextDelta {
		t.Fatalf("ev1 type = %v, want OutputTextDelta", ev1.Type)
	}

	ev2, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev2: %v", err)
	}
	if ev2.Type != domain.StreamEventCompleted {
		t.Fatalf("ev2 type = %v, want Completed", ev2.Type)
	}
	if ev2.FinishReason == nil || *ev2.FinishReason != "stop" {
		t.Fatalf("ev2 finish_reason = %v, want stop", ev2.FinishReason)
	}

	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestStream_RejectsNonzeroBaseResponse(t *testing.T) {
	stream := newTestStream("data: {\"base_resp\":{\"status_code\":17,\"status_msg\":\"provider error\"}}\n\n")
	_, err := stream.Recv()
	if err == nil {
		t.Fatal("expected provider envelope error")
	}
	gatewayErr, ok := err.(*domain.GatewayError)
	if !ok || gatewayErr.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("error = %#v", err)
	}
}

func TestStream_ReasoningContentDelta(t *testing.T) {
	sseData := "data: {\"id\":\"c3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"thinking\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n"

	body := fakeBody{strings.NewReader(sseData)}
	stream := NewStream(&http.Response{Body: body}, "deepseek")

	ev1, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev1: %v", err)
	}
	if ev1.Type != domain.StreamEventOutputTextDelta {
		t.Fatalf("ev1 type = %v, want OutputTextDelta", ev1.Type)
	}
	if ev1.ReasoningDelta == nil || *ev1.ReasoningDelta != "thinking" {
		t.Fatalf("ev1 reasoning = %v, want thinking", ev1.ReasoningDelta)
	}

	ev2, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev2: %v", err)
	}
	if ev2.ContentDelta == nil || *ev2.ContentDelta != "answer" {
		t.Fatalf("ev2 content = %v, want answer", ev2.ContentDelta)
	}
}

func TestStream_ReasoningContentIgnoredForNonDeepSeekProviders(t *testing.T) {
	sseData := "data: {\"id\":\"c4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"thinking\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n"

	stream := newTestStream(sseData)

	ev1, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev1: %v", err)
	}
	if ev1.ReasoningDelta != nil {
		t.Fatalf("ev1 reasoning = %v, want nil for non-DeepSeek provider", *ev1.ReasoningDelta)
	}

	ev2, err := stream.Recv()
	if err != nil {
		t.Fatalf("ev2: %v", err)
	}
	if ev2.ContentDelta == nil || *ev2.ContentDelta != "answer" {
		t.Fatalf("ev2 content = %v, want answer", ev2.ContentDelta)
	}
}

func TestStream_MapsDeepSeekCacheHitTokens(t *testing.T) {
	stream := newTestStream("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":9,\"total_tokens\":26,\"prompt_cache_hit_tokens\":7}}\n\n")

	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if event.Type != domain.StreamEventCompleted || event.Usage == nil {
		t.Fatalf("event = %+v, want completed event with usage", event)
	}
	if event.Usage.CacheReadTokens != 7 {
		t.Fatalf("cache-read tokens = %d, want 7", event.Usage.CacheReadTokens)
	}
}
