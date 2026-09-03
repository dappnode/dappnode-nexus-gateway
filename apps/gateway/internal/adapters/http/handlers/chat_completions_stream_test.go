package handlers

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type handlerTestStream struct {
	events []domain.StreamEvent
	err    error
	index  int
}

func (s *handlerTestStream) Recv() (domain.StreamEvent, error) {
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

			handler.writeStream(recorder, request, domain.GenerateRequest{PublicModelID: "test"}, &handlerTestStream{events: test.events}, nil)

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

	handler.writeStream(recorder, request, domain.GenerateRequest{PublicModelID: "test"}, &handlerTestStream{err: errors.New("upstream failed")}, nil)

	if strings.Contains(recorder.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("errored stream claimed completion: %q", recorder.Body.String())
	}
}
