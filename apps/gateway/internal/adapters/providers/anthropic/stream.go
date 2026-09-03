package anthropic

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Stream reads SSE events from an Anthropic streaming response.
type Stream struct {
	resp    *http.Response
	scanner *bufio.Scanner
	done    bool
	usage   *domain.Usage
	toolIdx int
}

func NewStream(resp *http.Response) *Stream {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &Stream{resp: resp, scanner: scanner}
}

func (s *Stream) Recv() (domain.StreamEvent, error) {
	if s.done {
		return domain.StreamEvent{}, io.EOF
	}

	var currentEvent string

	for s.scanner.Scan() {
		line := s.scanner.Text()

		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		event := s.processEvent(currentEvent, []byte(data))
		if event != nil {
			return *event, nil
		}
	}

	if err := s.scanner.Err(); err != nil {
		return domain.StreamEvent{}, err
	}

	s.done = true
	return domain.StreamEvent{}, io.EOF
}

func (s *Stream) Close() error {
	s.done = true
	return s.resp.Body.Close()
}

func (s *Stream) processEvent(eventType string, data []byte) *domain.StreamEvent {
	switch eventType {
	case "content_block_delta":
		var delta struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text,omitempty"`
				PartialJSON string `json:"partial_json,omitempty"`
			} `json:"delta"`
		}
		if json.Unmarshal(data, &delta) != nil {
			return nil
		}

		if delta.Delta.Type == "text_delta" {
			return &domain.StreamEvent{
				Type:         domain.StreamEventOutputTextDelta,
				ContentDelta: &delta.Delta.Text,
			}
		}
		if delta.Delta.Type == "input_json_delta" {
			return &domain.StreamEvent{
				Type: domain.StreamEventToolCallDelta,
				ToolCallDelta: &domain.ToolCallDelta{
					Index:          delta.Index,
					ArgumentsDelta: &delta.Delta.PartialJSON,
				},
			}
		}

	case "content_block_start":
		var block struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id,omitempty"`
				Name string `json:"name,omitempty"`
			} `json:"content_block"`
		}
		if json.Unmarshal(data, &block) != nil {
			return nil
		}
		if block.ContentBlock.Type == "tool_use" {
			return &domain.StreamEvent{
				Type: domain.StreamEventToolCallDelta,
				ToolCallDelta: &domain.ToolCallDelta{
					Index: block.Index,
					ID:    &block.ContentBlock.ID,
					Name:  &block.ContentBlock.Name,
				},
			}
		}

	case "message_start":
		var msg struct {
			Message struct {
				Usage struct {
					InputTokens              int64 `json:"input_tokens"`
					CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(data, &msg) == nil {
			// Normalize PromptTokens to total input (uncached + cache_creation + cache_read)
			totalInput := msg.Message.Usage.InputTokens + msg.Message.Usage.CacheCreationInputTokens + msg.Message.Usage.CacheReadInputTokens
			s.usage = &domain.Usage{
				PromptTokens:        totalInput,
				CacheCreationTokens: msg.Message.Usage.CacheCreationInputTokens,
				CacheReadTokens:     msg.Message.Usage.CacheReadInputTokens,
			}
		}
		role := "assistant"
		return &domain.StreamEvent{
			Type: domain.StreamEventOutputMessageDelta,
			Role: &role,
		}

	case "message_delta":
		var delta struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(data, &delta) != nil {
			return nil
		}

		if s.usage != nil {
			s.usage.CompletionTokens = delta.Usage.OutputTokens
			s.usage.TotalTokens = s.usage.PromptTokens + s.usage.CompletionTokens
		}

		finishReason := mapStopReason(delta.Delta.StopReason)
		return &domain.StreamEvent{
			Type:         domain.StreamEventCompleted,
			FinishReason: &finishReason,
			Usage:        s.usage,
		}

	case "message_stop":
		s.done = true
		return nil

	case "error":
		var errData struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		msg := "unknown error"
		if json.Unmarshal(data, &errData) == nil {
			msg = errData.Error.Message
		}
		return &domain.StreamEvent{
			Type: domain.StreamEventError,
			Error: &domain.GatewayError{
				HTTPStatus: 502,
				Type:       domain.ErrTypeProvider,
				Code:       domain.ErrCodeProviderUnavailable,
				Message:    msg,
			},
		}
	}

	return nil
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence":
		return "stop"
	default:
		return reason
	}
}
