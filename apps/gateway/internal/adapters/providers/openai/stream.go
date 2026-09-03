package openai

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Stream reads SSE events from an OpenAI-compatible streaming response.
type Stream struct {
	resp                    *http.Response
	scanner                 *bufio.Scanner
	done                    bool
	includeReasoningContent bool
	deferredCompleted       *domain.StreamEvent // stashed when tool-call delta + finish_reason arrive in one chunk
}

func NewStream(resp *http.Response, providerName ...string) *Stream {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	includeReasoningContent := len(providerName) > 0 && providerName[0] == "deepseek"
	return &Stream{
		resp:                    resp,
		scanner:                 scanner,
		includeReasoningContent: includeReasoningContent,
	}
}

func (s *Stream) Recv() (domain.StreamEvent, error) {
	if s.done {
		return domain.StreamEvent{}, io.EOF
	}

	// Return a stashed completion event from a previous chunk that carried
	// both a tool-call delta and a finish_reason.
	if s.deferredCompleted != nil {
		event := *s.deferredCompleted
		s.deferredCompleted = nil
		return event, nil
	}

	for s.scanner.Scan() {
		line := s.scanner.Text()

		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			s.done = true
			return domain.StreamEvent{}, io.EOF
		}

		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if err := providerBaseResponseError(chunk.BaseResp); err != nil {
			return domain.StreamEvent{}, err
		}
		events := mapChunkToStreamEvents(chunk, s.includeReasoningContent)
		if len(events) == 0 {
			continue
		}
		for i := range events {
			events[i].ProviderResponseID = chunk.ID
		}
		// If the mapper produced two events (tool-call delta + completed),
		// return the first now and stash the second for the next Recv().
		if len(events) > 1 {
			s.deferredCompleted = &events[1]
		}
		return events[0], nil
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

type chatCompletionChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string  `json:"role,omitempty"`
			Content          *string `json:"content,omitempty"`
			ReasoningContent *string `json:"reasoning_content,omitempty"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id,omitempty"`
				Type     string `json:"type,omitempty"`
				Function struct {
					Name      string `json:"name,omitempty"`
					Arguments string `json:"arguments,omitempty"`
				} `json:"function"`
			} `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens         int64 `json:"prompt_tokens"`
		CompletionTokens     int64 `json:"completion_tokens"`
		TotalTokens          int64 `json:"total_tokens"`
		PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
		PromptTokensDetails  *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details,omitempty"`
	} `json:"usage,omitempty"`
	BaseResp *providerBaseResponse `json:"base_resp,omitempty"`
}

type providerBaseResponse struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

func providerBaseResponseError(resp *providerBaseResponse) error {
	if resp == nil || resp.StatusCode == 0 {
		return nil
	}
	message := strings.TrimSpace(resp.StatusMsg)
	if message == "" {
		message = "provider returned an unsuccessful response"
	}
	return domain.ErrProviderError(http.StatusBadGateway, message).WithMeta(
		"upstream_code", resp.StatusCode,
		"upstream_error", message,
	)
}

func mapChunkToStreamEvents(chunk chatCompletionChunk, includeReasoningContent ...bool) []domain.StreamEvent {
	keepReasoning := len(includeReasoningContent) > 0 && includeReasoningContent[0]
	// Handle usage-only chunk (often last chunk with stream_options.include_usage)
	if len(chunk.Choices) == 0 && chunk.Usage != nil {
		return []domain.StreamEvent{{
			Type:  domain.StreamEventCompleted,
			Usage: chunkUsageToDomain(chunk.Usage),
		}}
	}

	if len(chunk.Choices) == 0 {
		return nil
	}

	choice := chunk.Choices[0]

	// Some providers (e.g. Novita/MiniMax) send reasoning_content chunks
	// with finish_reason set before the actual content chunk. If we honour
	// that finish_reason the stream closes before real content arrives.
	// Neutralise it so the subsequent content chunk carries the real signal.
	// The stream still terminates via upstream "data: [DONE]" / EOF even if
	// the later chunk happens to lack finish_reason.
	if choice.FinishReason != nil && choice.Delta.ReasoningContent != nil {
		hasContent := choice.Delta.Content != nil && *choice.Delta.Content != ""
		if !hasContent && len(choice.Delta.ToolCalls) == 0 {
			choice.FinishReason = nil
		}
	}

	// When a chunk carries both a tool-call delta AND a finish_reason (some
	// providers, e.g. MiniMax, pack the final argument fragment and the
	// finish signal into one chunk), we must emit the tool-call delta
	// FIRST so the client receives the complete JSON arguments before the
	// stream is marked done.
	if choice.FinishReason != nil && len(choice.Delta.ToolCalls) > 0 {
		tc := choice.Delta.ToolCalls[0]
		tcd := &domain.ToolCallDelta{
			Index: tc.Index,
		}
		if tc.ID != "" {
			tcd.ID = &tc.ID
		}
		if tc.Function.Name != "" {
			tcd.Name = &tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			tcd.ArgumentsDelta = &tc.Function.Arguments
		}
		completedEvent := domain.StreamEvent{
			Type:         domain.StreamEventCompleted,
			FinishReason: choice.FinishReason,
		}
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			completedEvent.ContentDelta = choice.Delta.Content
		}
		if keepReasoning && choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
			completedEvent.ReasoningDelta = choice.Delta.ReasoningContent
		}
		if chunk.Usage != nil {
			completedEvent.Usage = chunkUsageToDomain(chunk.Usage)
		}
		return []domain.StreamEvent{
			{
				Type:          domain.StreamEventToolCallDelta,
				ToolCallDelta: tcd,
			},
			completedEvent,
		}
	}

	// Check for finish reason -> completed event
	if choice.FinishReason != nil {
		event := domain.StreamEvent{
			Type:         domain.StreamEventCompleted,
			FinishReason: choice.FinishReason,
		}
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			event.ContentDelta = choice.Delta.Content
		}
		if keepReasoning && choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
			event.ReasoningDelta = choice.Delta.ReasoningContent
		}
		if chunk.Usage != nil {
			event.Usage = chunkUsageToDomain(chunk.Usage)
		}
		return []domain.StreamEvent{event}
	}

	// Tool call delta
	if len(choice.Delta.ToolCalls) > 0 {
		tc := choice.Delta.ToolCalls[0]
		tcd := &domain.ToolCallDelta{
			Index: tc.Index,
		}
		if tc.ID != "" {
			tcd.ID = &tc.ID
		}
		if tc.Function.Name != "" {
			tcd.Name = &tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			tcd.ArgumentsDelta = &tc.Function.Arguments
		}
		return []domain.StreamEvent{{
			Type:          domain.StreamEventToolCallDelta,
			ToolCallDelta: tcd,
		}}
	}

	// Text content delta
	if choice.Delta.Content != nil {
		event := domain.StreamEvent{
			Type:         domain.StreamEventOutputTextDelta,
			ContentDelta: choice.Delta.Content,
		}
		if keepReasoning {
			event.ReasoningDelta = choice.Delta.ReasoningContent
		}
		if choice.Delta.Role != "" {
			event.Role = &choice.Delta.Role
		}
		return []domain.StreamEvent{event}
	}

	// Reasoning content delta
	if keepReasoning && choice.Delta.ReasoningContent != nil {
		event := domain.StreamEvent{
			Type:           domain.StreamEventOutputTextDelta,
			ReasoningDelta: choice.Delta.ReasoningContent,
		}
		if choice.Delta.Role != "" {
			event.Role = &choice.Delta.Role
		}
		return []domain.StreamEvent{event}
	}

	// Role-only delta (first chunk often)
	if choice.Delta.Role != "" {
		role := choice.Delta.Role
		return []domain.StreamEvent{{
			Type: domain.StreamEventOutputMessageDelta,
			Role: &role,
		}}
	}

	return nil
}

func chunkUsageToDomain(u *struct {
	PromptTokens         int64 `json:"prompt_tokens"`
	CompletionTokens     int64 `json:"completion_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
	PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}) *domain.Usage {
	if u == nil {
		return nil
	}
	usage := &domain.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheReadTokens:  u.PromptCacheHitTokens,
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		usage.CacheReadTokens = u.PromptTokensDetails.CachedTokens
	}
	return usage
}
