package handlers

import (
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
	stream, model, err := h.service.ExecuteStream(r.Context(), genReq, token)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}
	defer stream.Close()
	h.writeStream(w, r, genReq, stream, model)
}

func (h *ChatCompletionsHandler) writeStream(
	w http.ResponseWriter,
	r *http.Request,
	genReq domain.GenerateRequest,
	stream ports.GenerationStream,
	model *domain.PublicModel,
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

		chunk, done := mapper.DomainStreamEventToChatChunk(event, responseModelID, responseID, createdAt)
		if chunk != nil {
			writeMu.Lock()
			sw.WriteData(chunk)
			writeMu.Unlock()
		}
		if done {
			writeMu.Lock()
			sw.WriteDone()
			writeMu.Unlock()
			// Keep draining so the usage-tracking wrapper can accumulate
			// the final usage chunk before io.EOF triggers recording.
			for {
				if _, err := stream.Recv(); err != nil {
					break
				}
			}
			break
		}
	}
}
