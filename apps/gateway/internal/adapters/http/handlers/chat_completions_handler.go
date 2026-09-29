package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/mapper"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/sse"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/google/uuid"
)

// Bound the wait for usage after generation has finished, without extending
// the lifetime of requests canceled before completion.
const streamUsageDrainTimeout = 5 * time.Second

// ChatCompletionsHandler handles POST /v1/chat/completions.
type ChatCompletionsHandler struct {
	service *services.ChatCompletionsService
	logger  ports.Logger
}

func NewChatCompletionsHandler(service *services.ChatCompletionsService, logger ports.Logger) *ChatCompletionsHandler {
	return &ChatCompletionsHandler{service: service, logger: logger}
}

func (h *ChatCompletionsHandler) Handle(w http.ResponseWriter, r *http.Request) {
	token, err := ExtractBearerToken(r)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, domain.ErrInvalidField("failed to read request body"))
		return
	}

	genReq, err := mapper.ChatCompletionRequestToDomain(body)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}

	// The gateway is a proxy: the request goes upstream as the client sent it
	// and the provider's response comes back as it came. Only requests that
	// need content rewritten (PII masking) or another wire format take the
	// translating path below.
	upstream, cancelUpstream := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancelUpstream()
	var finished atomic.Bool
	stopWatch := context.AfterFunc(r.Context(), func() {
		// A client gone after the finish reason still gets its usage metered:
		// the upstream is drained briefly instead of cut off.
		if finished.Load() {
			time.AfterFunc(streamUsageDrainTimeout, cancelUpstream)
			return
		}
		cancelUpstream()
	})
	defer stopWatch()
	call, err := h.service.Proxy(upstream, body, genReq, token)
	if err == nil {
		if call.Stream != nil {
			h.relay(w, r, call.Stream, &finished)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(call.Body)
		return
	}
	if !errors.Is(err, services.ErrNotProxyable) {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}

	if fields := mapper.UnknownChatCompletionFields(body); len(fields) > 0 {
		h.logger.Warn("chat completion request ignored unknown fields",
			"request_id", middleware.GetRequestID(r.Context()),
			"path", r.URL.Path,
			"fields", fields,
		)
	}

	if genReq.Stream {
		h.handleStream(w, r, genReq, token)
		return
	}

	result, err := h.service.Execute(r.Context(), genReq, token)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}

	resp := mapper.DomainToChatCompletionResponse(result)
	WriteJSON(w, http.StatusOK, resp)
}

// relay sends a provider's stream to the client line by line, as it came.
// Keep-alive comments fill long silences (slow reasoning), and a stream the
// provider finished without [DONE] gets one, so every client sees an end.
func (h *ChatCompletionsHandler) relay(w http.ResponseWriter, r *http.Request, stream *services.ProxyStream, finished *atomic.Bool) {
	defer stream.Close()
	sw, err := sse.NewWriter(w)
	if err != nil {
		WriteError(w, domain.ErrInternal("an internal error occurred"))
		return
	}
	var writeMu sync.Mutex
	lastWrite := time.Now()
	write := func(line []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()
		lastWrite = time.Now()
		// A client that left keeps the loop draining the upstream for usage.
		_ = sw.WriteLine(line)
	}
	stopKeepAlive := make(chan struct{})
	defer close(stopKeepAlive)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopKeepAlive:
				return
			case <-ticker.C:
				writeMu.Lock()
				if time.Since(lastWrite) >= 15*time.Second {
					lastWrite = time.Now()
					_ = sw.WriteComment("keepalive")
				}
				writeMu.Unlock()
			}
		}
	}()

	var readErr error
	for {
		line, err := stream.Next()
		if err != nil {
			readErr = err
			break
		}
		if stream.Finished() {
			finished.Store(true)
		}
		write(line)
	}
	switch {
	case stream.Complete() && !stream.SawDone():
		write([]byte("data: [DONE]"))
		write(nil)
	case readErr != io.EOF && r.Context().Err() == nil:
		// The upstream broke mid-response: say so rather than end quietly.
		h.logger.Warn("proxied stream interrupted", "request_id", middleware.GetRequestID(r.Context()))
		write([]byte(`data: {"error":{"type":"provider_error","code":"stream_interrupted","message":"The provider stream ended unexpectedly."}}`))
		write(nil)
	}
}

func (h *ChatCompletionsHandler) handleStream(w http.ResponseWriter, r *http.Request, genReq domain.GenerateRequest, token string) {
	ctx, cancelUpstream := context.WithCancel(r.Context())
	defer cancelUpstream()
	stream, model, err := h.service.ExecuteStream(ctx, genReq, token)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}
	defer stream.Close()
	h.writeStream(w, r, genReq, stream, model, cancelUpstream)
}

func (h *ChatCompletionsHandler) writeStream(
	w http.ResponseWriter,
	r *http.Request,
	genReq domain.GenerateRequest,
	stream ports.GenerationStream,
	model *domain.PublicModel,
	cancelUpstream context.CancelFunc,
) {
	responseModelID := genReq.PublicModelID
	if model != nil {
		responseModelID = model.PublicModelID
	}

	sw, err := sse.NewWriter(w)
	if err != nil {
		WriteError(w, domain.ErrInternal("an internal error occurred"))
		return
	}

	responseID := uuid.New().String()[:12]
	createdAt := time.Now().Unix()

	// Keep-alive: write SSE comments periodically to prevent proxy timeouts.
	var writeMu sync.Mutex
	stopKeepAlive := make(chan struct{})
	defer close(stopKeepAlive)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopKeepAlive:
				return
			case <-ticker.C:
				writeMu.Lock()
				sw.WriteComment("keepalive")
				writeMu.Unlock()
			}
		}
	}()

	for {
		event, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				writeMu.Lock()
				sw.WriteDone()
				writeMu.Unlock()
				break
			}
			writeMu.Lock()
			sw.WriteData(map[string]any{
				"error": map[string]any{"type": "internal_error", "message": err.Error()},
			})
			writeMu.Unlock()
			return
		}
		if event.ProviderResponseID != "" {
			responseID = event.ProviderResponseID
		}
		if event.Type == domain.StreamEventError {
			// A provider error after output started must not look like a
			// complete response: no finish chunk and no [DONE].
			message := "the provider stream failed"
			if event.Error != nil {
				message = event.Error.Error()
			}
			writeMu.Lock()
			sw.WriteData(map[string]any{
				"error": map[string]any{"type": "provider_error", "message": message},
			})
			writeMu.Unlock()
			return
		}
		write := func(event domain.StreamEvent) {
			chunk, _ := mapper.DomainStreamEventToChatChunk(event, responseModelID, responseID, createdAt)
			if chunk == nil {
				return
			}
			writeMu.Lock()
			writeErr := sw.WriteData(chunk)
			writeMu.Unlock()
			if writeErr != nil {
				h.logger.Warn("stream client write failed", "request_id", middleware.GetRequestID(r.Context()), "provider_request_id", responseID, "phase", "chunk", "write_failed", true)
			}
		}

		// Clients may close on finish_reason as well as [DONE]. Keep both
		// signals back until metering has consumed trailing usage and settled
		// the request. Canceling the upstream context safely unblocks reads
		// if a provider never terminates its post-completion stream.
		if event.Type == domain.StreamEventCompleted {
			// Output the finishing chunk carried goes out now; only the finish
			// signal waits.
			for _, delta := range splitDeltas(&event) {
				write(delta)
			}
			requestID := middleware.GetRequestID(r.Context())
			providerID := responseID
			timer := time.AfterFunc(streamUsageDrainTimeout, func() {
				h.logger.Warn("stream finalization timeout", "request_id", requestID, "provider_request_id", providerID, "model", responseModelID, "timeout_ms", streamUsageDrainTimeout.Milliseconds())
				cancelUpstream()
			})
			for {
				tail, err := stream.Recv()
				if err != nil {
					break
				}
				if tail.Usage != nil {
					event.Usage = tail.Usage
				}
				if tail.FinishReason != nil {
					event.FinishReason = tail.FinishReason
				}
				// Output after the finish reason is still the response.
				for _, delta := range splitDeltas(&tail) {
					write(delta)
				}
			}
			timer.Stop()
		}
		chunk, done := mapper.DomainStreamEventToChatChunk(event, responseModelID, responseID, createdAt)
		if chunk != nil {
			writeMu.Lock()
			writeErr := sw.WriteData(chunk)
			writeMu.Unlock()
			if writeErr != nil {
				h.logger.Warn("stream client write failed", "request_id", middleware.GetRequestID(r.Context()), "provider_request_id", responseID, "phase", "chunk", "write_failed", true)
			}
		}
		if done {
			writeMu.Lock()
			writeErr := sw.WriteDone()
			writeMu.Unlock()
			if writeErr != nil {
				h.logger.Warn("stream client write failed", "request_id", middleware.GetRequestID(r.Context()), "provider_request_id", responseID, "phase", "done", "write_failed", true)
			}
			h.logger.Debug("stream completion sent", "request_id", middleware.GetRequestID(r.Context()), "provider_request_id", responseID, "done_write_failed", writeErr != nil, "client_context_canceled", r.Context().Err() != nil)
			break
		}
	}
}

// splitDeltas moves the text, reasoning, and tool-call output out of an event
// into delta events of their own, leaving the rest (finish, usage) in place.
func splitDeltas(event *domain.StreamEvent) []domain.StreamEvent {
	var out []domain.StreamEvent
	content := event.ContentDelta != nil && *event.ContentDelta != ""
	reasoning := event.ReasoningDelta != nil && *event.ReasoningDelta != ""
	if content || reasoning {
		delta := domain.StreamEvent{Type: domain.StreamEventOutputTextDelta}
		if content {
			delta.ContentDelta = event.ContentDelta
		}
		if reasoning {
			delta.ReasoningDelta = event.ReasoningDelta
		}
		out = append(out, delta)
	}
	if event.ToolCallDelta != nil {
		out = append(out, domain.StreamEvent{Type: domain.StreamEventToolCallDelta, ToolCallDelta: event.ToolCallDelta})
	}
	event.ContentDelta, event.ReasoningDelta, event.ToolCallDelta = nil, nil, nil
	return out
}
