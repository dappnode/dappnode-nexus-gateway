package handlers

import (
	"io"
	"net/http"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/mapper"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type SystemOneHandler struct {
	service *services.GenerateService
	logger  ports.Logger
}

func NewSystemOneHandler(service *services.GenerateService, logger ports.Logger) *SystemOneHandler {
	return &SystemOneHandler{service: service, logger: logger}
}

func (h *SystemOneHandler) Handle(w http.ResponseWriter, r *http.Request) {
	token, err := ExtractBearerToken(r)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodySize))
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, domain.ErrInvalidField("request body unreadable or too large"))
		return
	}
	req, err := mapper.SystemOneRequestToDomain(body)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}
	call, err := h.service.Proxy(r.Context(), domain.EndpointSystemOne, body, req, token)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(call.Body)
}
