package handlers

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type handlerTestStream struct {
	events     []domain.StreamEvent
	err        error
	index      int
	beforeRecv func()
}

func (s *handlerTestStream) Recv() (domain.StreamEvent, error) {
	if s.beforeRecv != nil {
		s.beforeRecv()
	}
	if s.index < len(s.events) {
		event := s.events[s.index]
		s.index++
		return event, nil
	}
	if s.err != nil {
		return domain.StreamEvent{}, s.err
	}
	return domain.StreamEvent{}, io.EOF
}

func TestChatCompletionsHandler_SettlesBeforeEmittingFinish(t *testing.T) {
	finish := "tool_calls"
	recorder := httptest.NewRecorder()
	stream := &handlerTestStream{
		events: []domain.StreamEvent{
			{Type: domain.StreamEventCompleted, FinishReason: &finish},
			{Type: domain.StreamEventCompleted, Usage: &domain.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}},
		},
		beforeRecv: func() {
			if strings.Contains(recorder.Body.String(), `"finish_reason"`) || strings.Contains(recorder.Body.String(), "[DONE]") {
				t.Error("client received a completion signal before upstream EOF")
			}
		},
	}
	handler := &ChatCompletionsHandler{logger: &confidentialTestLogger{}}
	handler.writeStream(recorder, httptest.NewRequest("POST", "/v1/chat/completions", nil), domain.GenerateRequest{PublicModelID: "test"}, stream, nil, func() {})
	if !strings.Contains(recorder.Body.String(), `"prompt_tokens":100`) || !strings.Contains(recorder.Body.String(), `"finish_reason":"tool_calls"`) {
		t.Fatalf("missing final usage or finish: %s", recorder.Body.String())
	}
	if strings.Count(recorder.Body.String(), "data: [DONE]") != 1 {
		t.Fatalf("expected exactly one DONE: %s", recorder.Body.String())
	}
}

type stalledCompletionStream struct {
	handlerTestStream
	ctx context.Context
}

func (s *stalledCompletionStream) Recv() (domain.StreamEvent, error) {
	if s.index < len(s.events) {
		return s.handlerTestStream.Recv()
	}
	<-s.ctx.Done()
	return domain.StreamEvent{}, s.ctx.Err()
}

func TestChatCompletionsHandler_BoundsTrailingUsageWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), streamUsageDrainTimeout+2*time.Second)
	defer cancel()
	finish := "tool_calls"
	stream := &stalledCompletionStream{
		handlerTestStream: handlerTestStream{events: []domain.StreamEvent{{Type: domain.StreamEventCompleted, FinishReason: &finish}}},
		ctx:               ctx,
	}
	recorder := httptest.NewRecorder()
	log := &finalizationLogRecorder{warnings: make(chan string, 1)}
	handler := &ChatCompletionsHandler{logger: log}
	handler.writeStream(recorder, httptest.NewRequest("POST", "/v1/chat/completions", nil), domain.GenerateRequest{PublicModelID: "test"}, stream, nil, cancel)
	select {
	case message := <-log.warnings:
		if message != "stream finalization timeout" {
			t.Fatalf("unexpected warning: %s", message)
		}
	default:
		t.Fatal("finalization timeout was not logged")
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("stalled upstream was not canceled")
	}
	if !strings.Contains(recorder.Body.String(), `"finish_reason":"tool_calls"`) || strings.Contains(recorder.Body.String(), `"usage"`) {
		t.Fatalf("expected finished generation without fabricated usage: %s", recorder.Body.String())
	}
}

func (*handlerTestStream) Close() error { return nil }

func TestChatCompletionsHandler_StreamEmitsExactlyOneDoneOnCleanEOF(t *testing.T) {
	content := "hello"
	finishReason := "stop"
	tests := []struct {
		name   string
		events []domain.StreamEvent
	}{
		{
			name: "upstream done marker becomes EOF",
			events: []domain.StreamEvent{{
				Type:         domain.StreamEventOutputTextDelta,
				ContentDelta: &content,
			}},
		},
		{
			name: "completed event followed by EOF",
			events: []domain.StreamEvent{{
				Type:         domain.StreamEventCompleted,
				FinishReason: &finishReason,
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &ChatCompletionsHandler{logger: &confidentialTestLogger{}}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/v1/chat/completions", nil)

			handler.writeStream(recorder, request, domain.GenerateRequest{PublicModelID: "test"}, &handlerTestStream{events: test.events}, nil, func() {})

			if count := strings.Count(recorder.Body.String(), "data: [DONE]\n\n"); count != 1 {
				t.Fatalf("DONE marker count = %d, want 1; stream = %q", count, recorder.Body.String())
			}
		})
	}
}

func TestChatCompletionsHandler_StreamErrorDoesNotClaimCompletion(t *testing.T) {
	handler := &ChatCompletionsHandler{logger: &confidentialTestLogger{}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	handler.writeStream(recorder, request, domain.GenerateRequest{PublicModelID: "test"}, &handlerTestStream{err: errors.New("upstream failed")}, nil, func() {})

	if strings.Contains(recorder.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("errored stream claimed completion: %q", recorder.Body.String())
	}
}

// A channel keeps the timer's logging callback safe to observe from the test.
type finalizationLogRecorder struct {
	confidentialTestLogger
	warnings chan string
}

func (l *finalizationLogRecorder) Warn(message string, _ ...any) { l.warnings <- message }

func TestChatCompletionsHandler_ForwardsOutputAroundTheFinishReason(t *testing.T) {
	finish, early, late, args := "tool_calls", "Reading.", "late text", "{}"
	id, name := "call_1", "read"
	stream := &handlerTestStream{events: []domain.StreamEvent{
		{Type: domain.StreamEventCompleted, FinishReason: &finish, ContentDelta: &early},
		{Type: domain.StreamEventToolCallDelta, ToolCallDelta: &domain.ToolCallDelta{Index: 0, ID: &id, Name: &name, ArgumentsDelta: &args}},
		{Type: domain.StreamEventOutputTextDelta, ContentDelta: &late},
	}}
	recorder := httptest.NewRecorder()
	handler := &ChatCompletionsHandler{logger: &confidentialTestLogger{}}
	handler.writeStream(recorder, httptest.NewRequest("POST", "/v1/chat/completions", nil), domain.GenerateRequest{PublicModelID: "test"}, stream, nil, func() {})
	body := recorder.Body.String()
	order := []string{`"content":"Reading."`, `"id":"call_1"`, `"content":"late text"`, `"finish_reason":"tool_calls"`, "[DONE]"}
	at := 0
	for _, want := range order {
		i := strings.Index(body[at:], want)
		if i < 0 {
			t.Fatalf("missing %s in order: %s", want, body)
		}
		at += i
	}
}

func TestChatCompletionsHandler_ProviderErrorEventIsNotCompletion(t *testing.T) {
	content := "partial"
	stream := &handlerTestStream{events: []domain.StreamEvent{
		{Type: domain.StreamEventOutputTextDelta, ContentDelta: &content},
		{Type: domain.StreamEventError, Error: domain.ErrProviderError(502, "overloaded")},
	}}
	recorder := httptest.NewRecorder()
	handler := &ChatCompletionsHandler{logger: &confidentialTestLogger{}}
	handler.writeStream(recorder, httptest.NewRequest("POST", "/v1/chat/completions", nil), domain.GenerateRequest{PublicModelID: "test"}, stream, nil, func() {})
	body := recorder.Body.String()
	if strings.Contains(body, "[DONE]") || strings.Contains(body, `"finish_reason":"`) || !strings.Contains(body, `"error"`) {
		t.Fatalf("error event reported as completion: %s", body)
	}
}

type brokenBody struct{ io.Reader }

func (brokenBody) Close() error { return nil }

type failAfter struct {
	r   io.Reader
	err error
}

func (f *failAfter) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err == io.EOF {
		return n, f.err
	}
	return n, err
}

func TestRelay_EndsEveryStream(t *testing.T) {
	finishedNoDone := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"
	for name, tc := range map[string]struct {
		body io.Reader
		want []string
		not  []string
	}{
		"relayed as sent":                {body: strings.NewReader(finishedNoDone + "data: [DONE]\n\n"), want: []string{`"content":"hi"`}},
		"finish without [DONE] gets one": {body: strings.NewReader(finishedNoDone), want: []string{"data: [DONE]"}},
		"broken mid-response says so": {
			body: &failAfter{r: strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"par\"}}]}\n\n"), err: errors.New("connection reset")},
			want: []string{"stream_interrupted"}, not: []string{"[DONE]"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler := &ChatCompletionsHandler{logger: &confidentialTestLogger{}}
			var finished atomic.Bool
			handler.relay(recorder, httptest.NewRequest("POST", "/v1/chat/completions", nil), services.NewProxyStream(brokenBody{tc.body}, "m"), &finished)
			body := recorder.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Fatalf("missing %q in %q", want, body)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(body, not) {
					t.Fatalf("unexpected %q in %q", not, body)
				}
			}
			if strings.Count(body, "data: [DONE]") > 1 {
				t.Fatalf("[DONE] twice: %q", body)
			}
		})
	}
}
