package openai

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// maxSSELine bounds one SSE line. Providers that do not stream tool
// arguments send a whole call (for example a large file) in one line.
const maxSSELine = 16 << 20

// Stream reads SSE events from an OpenAI-compatible streaming response and
// normalizes them, so every client sees the same well-formed shape whatever
// the provider sends:
//
//   - every tool call in a chunk is forwarded, not only the first;
//   - each tool call gets contiguous indexes, one id (synthesized if the
//     provider omits it), and its id and name only once;
//   - a call that received no arguments gets "{}";
//   - text in a chunk that also carries tool calls is kept;
//   - a usage-only chunk before the finish reason does not end the stream;
//   - a stream that ends without a finish reason gets one, while a stream with
//     no chunk at all or an error payload is reported as a provider error.
type Stream struct {
	diagnostics             *streamDiagnostics
	resp                    *http.Response
	scanner                 *bufio.Scanner
	done                    bool // upstream fully read
	includeReasoningContent bool
	pending                 []domain.StreamEvent
	tools                   toolCallNormalizer
	completed               bool // a finish reason was forwarded
	sawChunk                bool
	lastUsage               *domain.Usage
}

func NewStream(resp *http.Response, providerName ...string) *Stream {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELine)
	includeReasoningContent := len(providerName) > 0 && providerName[0] == "deepseek"
	return &Stream{
		resp:                    resp,
		scanner:                 scanner,
		includeReasoningContent: includeReasoningContent,
	}
}

func (s *Stream) Recv() (domain.StreamEvent, error) {
	for len(s.pending) == 0 {
		if s.done {
			return domain.StreamEvent{}, io.EOF
		}
		events, err := s.read()
		if err != nil {
			return domain.StreamEvent{}, err
		}
		s.pending = events
	}
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event, nil
}

// read consumes upstream lines until they produce events or the stream ends.
func (s *Stream) read() ([]domain.StreamEvent, error) {
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			continue
		}
		// SSE allows "data:" with or without a following space.
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			if s.diagnostics != nil && !isSSEField(line) {
				s.diagnostics.unsupported++
			}
			continue
		}
		data = strings.TrimPrefix(data, " ")
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			s.diagnostics.end("done_marker", nil)
			return s.end()
		}

		if s.diagnostics != nil {
			s.diagnostics.chunks++
		}
		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			if s.diagnostics != nil {
				s.diagnostics.malformed++
			}
			continue
		}
		s.sawChunk = true
		if err := providerBaseResponseError(chunk.BaseResp); err != nil {
			return nil, s.fail(chunk, err)
		}
		if err := providerStreamError(chunk.Error); err != nil {
			return nil, s.fail(chunk, err)
		}
		events := s.normalize(mapChunkToStreamEvents(chunk, s.includeReasoningContent))
		if s.diagnostics != nil {
			s.diagnostics.observe(chunk, events)
		}
		for i := range events {
			events[i].ProviderResponseID = chunk.ID
		}
		if len(events) > 0 {
			return events, nil
		}
	}

	if err := s.scanner.Err(); err != nil {
		s.diagnostics.end("read_error", err)
		return nil, err
	}
	s.diagnostics.end("eof", nil)
	return s.end()
}

// isSSEField reports SSE lines other than data: comments, event, id, retry.
func isSSEField(line string) bool {
	return strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") ||
		strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:")
}

func (s *Stream) fail(chunk chatCompletionChunk, err error) error {
	if s.diagnostics != nil {
		s.diagnostics.observe(chunk, nil)
	}
	s.diagnostics.end("provider_error", err)
	s.done = true
	return err
}

// end finishes a stream the provider closed. A response without any chunk is
// a provider failure (so fallback can run); a response without a finish
// reason gets one, so clients always see a complete message.
func (s *Stream) end() ([]domain.StreamEvent, error) {
	s.done = true
	if !s.sawChunk {
		return nil, domain.ErrProviderError(http.StatusBadGateway, "provider returned an empty stream")
	}
	if s.completed {
		return nil, nil
	}
	finish := "stop"
	return s.normalize([]domain.StreamEvent{{Type: domain.StreamEventCompleted, FinishReason: &finish, Usage: s.lastUsage}}), nil
}

// normalize applies the per-stream rules described on Stream.
func (s *Stream) normalize(events []domain.StreamEvent) []domain.StreamEvent {
	out := make([]domain.StreamEvent, 0, len(events)+1)
	for _, event := range events {
		if event.Usage != nil {
			s.lastUsage = event.Usage
		}
		switch {
		case event.ToolCallDelta != nil:
			event.ToolCallDelta = s.tools.normalize(event.ToolCallDelta)
			if event.ToolCallDelta == nil {
				if event.Usage == nil {
					continue
				}
				event.Type = domain.StreamEventOutputMessageDelta
			}
		case event.Type == domain.StreamEventCompleted && s.completed:
			// Trailing usage after the finish reason.
		case event.Type == domain.StreamEventCompleted && event.FinishReason == nil:
			// Usage-only chunk before the model finished: not the end.
			event.Type = domain.StreamEventOutputMessageDelta
		case event.Type == domain.StreamEventCompleted:
			out = append(out, s.tools.fillEmptyArguments()...)
			if event.Usage == nil {
				event.Usage = s.lastUsage // Usage sent before the finish reason.
			}
			if s.tools.seen() && *event.FinishReason == "stop" {
				finish := "tool_calls"
				event.FinishReason = &finish
			}
			s.completed = true
		}
		out = append(out, event)
	}
	return out
}

func (s *Stream) Close() error {
	s.diagnostics.end("closed", nil)
	s.done = true
	return s.resp.Body.Close()
}

// toolCallNormalizer turns provider tool-call fragments into the OpenAI shape:
// indexes 0..n-1 in order of appearance, the id, type, and name on the first
// fragment only, and arguments as they stream. Providers differ: some pack
// several calls in one chunk, some repeat the id and name on every fragment,
// some reuse index 0 for every call with distinct ids, and some omit the id.
type toolCallNormalizer struct {
	byIndex map[int]*toolCallSlot
	slots   []*toolCallSlot
}

type toolCallSlot struct {
	index     int
	id, name  string
	arguments int
}

func (n *toolCallNormalizer) seen() bool { return len(n.slots) > 0 }

func (n *toolCallNormalizer) normalize(in *domain.ToolCallDelta) *domain.ToolCallDelta {
	if n.byIndex == nil {
		n.byIndex = map[int]*toolCallSlot{}
	}
	id := ""
	if in.ID != nil {
		id = *in.ID
	}
	slot := n.byIndex[in.Index]
	if slot != nil && id != "" && id != slot.id {
		slot = nil // A new call that reuses the index.
	}
	out := &domain.ToolCallDelta{}
	if slot == nil {
		slot = &toolCallSlot{index: len(n.slots), id: id}
		if slot.id == "" {
			slot.id = newToolCallID()
		}
		n.slots = append(n.slots, slot)
		n.byIndex[in.Index] = slot
		out.ID = &slot.id
	}
	out.Index = slot.index
	if in.Name != nil && *in.Name != "" && *in.Name != slot.name {
		name := *in.Name
		if slot.name != "" {
			if !strings.HasPrefix(name, slot.name) {
				slot.name += name // Streamed in pieces.
			} else {
				name = strings.TrimPrefix(name, slot.name) // Repeated with more text.
				slot.name += name
			}
		} else {
			slot.name = name
		}
		if name != "" {
			out.Name = &name
		}
	}
	if in.ArgumentsDelta != nil && *in.ArgumentsDelta != "" {
		arguments := *in.ArgumentsDelta
		slot.arguments += len(arguments)
		out.ArgumentsDelta = &arguments
	}
	if out.ID == nil && out.Name == nil && out.ArgumentsDelta == nil {
		return nil
	}
	return out
}

// fillEmptyArguments gives calls that received no arguments "{}", which is
// what OpenAI sends and what strict clients parse.
func (n *toolCallNormalizer) fillEmptyArguments() []domain.StreamEvent {
	var events []domain.StreamEvent
	for _, slot := range n.slots {
		if slot.arguments == 0 {
			slot.arguments = 2
			empty := "{}"
			events = append(events, domain.StreamEvent{
				Type:          domain.StreamEventToolCallDelta,
				ToolCallDelta: &domain.ToolCallDelta{Index: slot.index, ArgumentsDelta: &empty},
			})
		}
	}
	return events
}

func newToolCallID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "call_" + hex.EncodeToString(b)
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
	Error    *providerStreamErr    `json:"error,omitempty"`
}

// providerStreamErr is an error some providers send as a data line after the
// stream started (overload, context length, moderation).
type providerStreamErr struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func providerStreamError(e *providerStreamErr) error {
	if e == nil {
		return nil
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "provider stream failed"
	}
	return domain.ErrProviderError(http.StatusBadGateway, message).WithMeta(
		"upstream_error_type", e.Type,
		"upstream_code", e.Code,
		"upstream_error", message,
	)
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

func mapChunkToStreamEvents(chunk chatCompletionChunk, includeReasoningContent ...bool) (events []domain.StreamEvent) {
	// Usage is independent of the delta shape. Some compatible providers put
	// it on text, role, or tool deltas instead of the final usage-only chunk.
	defer func() {
		if chunk.Usage == nil {
			return
		}
		if len(events) == 0 {
			// Preserve usage even when the chunk has no visible delta. A role
			// event carries it to metering without claiming generation is done.
			events = []domain.StreamEvent{{Type: domain.StreamEventOutputMessageDelta}}
		}
		events[0].Usage = chunkUsageToDomain(chunk.Usage)
	}()
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

	// Every tool call in the chunk, in order. Some providers pack several
	// calls, or a whole call, into one chunk.
	toolEvents := make([]domain.StreamEvent, 0, len(choice.Delta.ToolCalls))
	for _, tc := range choice.Delta.ToolCalls {
		tcd := &domain.ToolCallDelta{Index: tc.Index}
		if tc.ID != "" {
			tcd.ID = &tc.ID
		}
		if tc.Function.Name != "" {
			tcd.Name = &tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			tcd.ArgumentsDelta = &tc.Function.Arguments
		}
		toolEvents = append(toolEvents, domain.StreamEvent{Type: domain.StreamEventToolCallDelta, ToolCallDelta: tcd})
	}
	// Text that shares a chunk with tool calls comes first, as generated.
	var text []domain.StreamEvent
	if len(toolEvents) > 0 {
		content := choice.Delta.Content != nil && *choice.Delta.Content != ""
		reasoning := keepReasoning && choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != ""
		if content || reasoning {
			event := domain.StreamEvent{Type: domain.StreamEventOutputTextDelta}
			if content {
				event.ContentDelta = choice.Delta.Content
			}
			if reasoning {
				event.ReasoningDelta = choice.Delta.ReasoningContent
			}
			text = append(text, event)
		}
	}

	// When a chunk carries tool-call deltas AND a finish_reason (e.g.
	// MiniMax packs the final argument fragment and the finish signal into
	// one chunk), the deltas go first so the client has complete arguments
	// before the stream is marked done.
	if choice.FinishReason != nil && len(toolEvents) > 0 {
		completedEvent := domain.StreamEvent{
			Type:         domain.StreamEventCompleted,
			FinishReason: choice.FinishReason,
		}
		if chunk.Usage != nil {
			completedEvent.Usage = chunkUsageToDomain(chunk.Usage)
		}
		events = append(text, toolEvents...)
		return append(events, completedEvent)
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

	if len(toolEvents) > 0 {
		return append(text, toolEvents...)
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
