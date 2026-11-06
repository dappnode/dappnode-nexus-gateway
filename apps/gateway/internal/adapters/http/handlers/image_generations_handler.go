package handlers

import (
	"io"
	"net/http"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/mapper"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// ImageGenerationsHandler handles POST /v1/images/generations, the OpenAI
// images API. Images come back inline (b64_json) and are not stored.
type ImageGenerationsHandler struct {
	service *services.GenerateService
	logger  ports.Logger
}

func NewImageGenerationsHandler(service *services.GenerateService, logger ports.Logger) *ImageGenerationsHandler {
	return &ImageGenerationsHandler{service: service, logger: logger}
}

func (h *ImageGenerationsHandler) Handle(w http.ResponseWriter, r *http.Request) {
	token, err := ExtractBearerToken(r)
	if err != nil {
		rejectRequest(w, r, h.logger, domain.EndpointImageGenerations, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
		rejectRequest(w, r, h.logger, domain.EndpointImageGenerations, domain.ErrInvalidField("failed to read request body"))
		return
	}
	req, err := mapper.ImageGenerationRequestToDomain(body)
	if err != nil {
		rejectRequest(w, r, h.logger, domain.EndpointImageGenerations, err)
		return
	}
	call, err := h.service.GenerateImages(r.Context(), body, req, token, strings.TrimSpace(r.Header.Get("Idempotency-Key")))
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if call.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(call.Body)
}
