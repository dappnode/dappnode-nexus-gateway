package handlers

import (
	"context"
	"io"
	"net/http"
	"sync"
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

	if fields := mapper.UnknownChatCompletionFields(body); len(fields) > 0 {
		h.logger.Warn("chat completion request ignored unknown fields",
			"request_id", middleware.GetRequestID(r.Context()),
			"path", r.URL.Path,
			"fields", fields,
		)
	}

	genReq, err := mapper.ChatCompletionRequestToDomain(body)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
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
