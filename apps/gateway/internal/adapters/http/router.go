package http

import (
	"net/http"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/handlers"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
)

// NewRouter builds the HTTP mux with all routes and middleware.
func NewRouter(
	health *handlers.HealthHandler,
	models *handlers.ModelsHandler,
	chatCompletions *handlers.ChatCompletionsHandler,
	confidentialChat *handlers.ConfidentialChatCompletionsHandler,
	tinfoil *handlers.TinfoilHandler,
	attestation *handlers.AttestationHandler,
	logger ports.Logger,
) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", health.Handle)
	mux.HandleFunc("GET /v1/models", models.Handle)
	mux.HandleFunc("POST /v1/chat/completions", chatCompletions.Handle)
	if confidentialChat != nil {
		mux.HandleFunc("POST /v1/confidential/chat/completions", confidentialChat.Handle)
	}
	if tinfoil != nil {
		mux.HandleFunc("GET /v1/tinfoil/proofs/{response_id}", tinfoil.HandleGetProof)
	}
	if attestation != nil {
		mux.HandleFunc("POST /v1/attestation", attestation.Handle)
	}

	var handler http.Handler = mux
	handler = middleware.CORS(handler)
	handler = middleware.Logging(logger)(handler)
	handler = middleware.Recovery(logger)(handler)
	handler = middleware.RequestID(handler)
	handler = middleware.Metrics(handler)

	return handler
}
