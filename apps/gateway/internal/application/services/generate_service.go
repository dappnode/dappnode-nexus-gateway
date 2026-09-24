package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// GenerateService runs chat-completions requests: it authenticates, routes,
// validates, masks PII, reserves credit, forwards to the provider, and meters.
type GenerateService struct {
	auth          ports.AuthService
	catalog       ports.ModelCatalog
	router        ports.RouterClient
	registry      ports.ProviderRegistry
	metering      ports.UsageMeter
	pii           ports.PIIFilter
	piiLang       string
	piiFailOpen   bool
	tinfoilProofs ports.TinfoilTransportProofRepository
	logger        ports.Logger
}

func NewGenerateService(
	auth ports.AuthService,
	catalog ports.ModelCatalog,
	router ports.RouterClient,
	registry ports.ProviderRegistry,
	metering ports.UsageMeter,
	pii ports.PIIFilter,
	logger ports.Logger,
) *GenerateService {
	return &GenerateService{
		auth:     auth,
		catalog:  catalog,
		router:   router,
		registry: registry,
		metering: metering,
		pii:      pii,
		piiLang:  "en",
		logger:   logger,
	}
}

// SetPIIOptions configures the PII filter language and failure mode.
// failOpen=true allows requests through when the filter is unreachable.
func (s *GenerateService) SetPIIOptions(language string, failOpen bool) {
	if language != "" {
		s.piiLang = language
	}
	s.piiFailOpen = failOpen
}

func (s *GenerateService) SetTinfoilProofRepository(proofs ports.TinfoilTransportProofRepository) {
	s.tinfoilProofs = proofs
}

func recordUpstreamLatency(publicModelID, providerName string, start time.Time, err error) {
	outcome := metrics.OutcomeSuccess
	if err != nil {
		outcome = metrics.OutcomeError
	}
	metrics.UpstreamLatency.WithLabelValues(providerName, publicModelID, outcome).Observe(time.Since(start).Seconds())
}

func shouldTryFallback(ctx context.Context, err error, fallback *domain.ProviderTarget) bool {
	if err == nil || fallback == nil || ctx.Err() != nil {
		return false
	}
	var gatewayErr *domain.GatewayError
	return !errors.As(err, &gatewayErr) || gatewayErr.Code != domain.ErrCodeClientCanceled
}

func withProviderTarget(model domain.PublicModel, target domain.ProviderTarget) domain.PublicModel {
	model.ProviderModelID = target.ProviderModelID
	model.UpstreamModelName = target.UpstreamModelName
	model.ServiceTier = target.ServiceTier
	model.ProviderConfig = target.ProviderConfig
	model.Fallback = nil
	return model
}

func (s *GenerateService) logFallback(requestID string, primary, fallback domain.PublicModel, err error) {
	if s.logger == nil {
		return
	}
	s.logger.Warn("provider failed; trying fallback",
		"request_id", requestID,
		"provider", primary.ProviderConfig.ProviderName,
		"provider_model", primary.UpstreamModelName,
		"fallback_provider", fallback.ProviderConfig.ProviderName,
		"fallback_provider_model", fallback.UpstreamModelName,
		"error_type", fmt.Sprintf("%T", err),
	)
}

func (s *GenerateService) resolveModel(ctx context.Context, req domain.GenerateRequest) (domain.PublicModel, domain.GenerateRequest, error) {
	requestedModelID := req.PublicModelID
	if req.RequestedModelID == "" {
		req.RequestedModelID = requestedModelID
	}

	model, err := s.catalog.GetPublicModel(ctx, requestedModelID)
	if err == nil {
		return model, req, nil
	}
	if !isUnsupportedModelError(err) {
		return domain.PublicModel{}, req, err
	}

	routerEntry, routerErr := s.catalog.GetRouter(ctx, requestedModelID)
	if routerErr != nil {
		if isNotFoundError(routerErr) {
			return domain.PublicModel{}, req, domain.ErrUnsupportedModel(requestedModelID)
		}
		return domain.PublicModel{}, req, routerErr
	}
	if s.router == nil {
		return domain.PublicModel{}, req, domain.ErrInternal("an internal error occurred").WithMeta(
			"dependency", "router",
			"reason", "router client is not configured",
		)
	}

	decision, err := s.router.Route(ctx, domain.RouteRequest{
		RouterID: routerEntry.RouterID,
		Request:  req,
	})
	if err != nil {
		return domain.PublicModel{}, req, err
	}
	if decision.PublicModelID == "" {
		return domain.PublicModel{}, req, domain.ErrInternal("an internal error occurred").WithMeta(
			"dependency", "router",
			"reason", "router returned an empty public_model_id",
		)
	}

	model, err = s.catalog.GetPublicModel(ctx, decision.PublicModelID)
	if err != nil {
		if isUnsupportedModelError(err) {
			return domain.PublicModel{}, req, domain.ErrUnsupportedModel(decision.PublicModelID)
		}
		return domain.PublicModel{}, req, err
	}

	routerID := routerEntry.RouterID
	routedPublicModelID := decision.PublicModelID
	req.PublicModelID = decision.PublicModelID
	req.RouterID = &routerID
	req.RoutedPublicModelID = &routedPublicModelID
	if decision.Category != nil {
		req.MatchedCategory = decision.Category
	}
	req.RoutingScore = decision.Score
	if len(decision.CategoryScores) > 0 {
		req.RoutingCategoryScores = append([]domain.RoutingCategoryScore(nil), decision.CategoryScores...)
	}
	if decision.Reason != "" {
		reason := decision.Reason
		req.DecisionReason = &reason
	}
	fallback := decision.FallbackUsed
	req.FallbackUsed = &fallback
	return model, req, nil
}

func isUnsupportedModelError(err error) bool {
	var gwErr *domain.GatewayError
	return errors.As(err, &gwErr) && gwErr.Code == domain.ErrCodeUnsupportedModel
}

func isNotFoundError(err error) bool {
	var gwErr *domain.GatewayError
	return errors.As(err, &gwErr) && gwErr.Code == domain.ErrCodeNotFound
}

func (s *GenerateService) validateRequest(endpoint string, req domain.GenerateRequest, model domain.PublicModel) error {
	if !model.SupportsEndpoint(endpoint) {
		return domain.ErrUnsupportedEndpoint(model.PublicModelID, endpoint)
	}

	if req.Stream && !model.SupportsStreamForEndpoint(endpoint) {
		return domain.ErrUnsupportedFeature("streaming for " + endpoint)
	}

	if len(req.Tools) > 0 && !model.SupportsTools {
		return domain.ErrUnsupportedFeature("tools")
	}

	if req.ParallelToolCalls != nil && *req.ParallelToolCalls && !model.SupportsParallelToolCalls {
		return domain.ErrUnsupportedFeature("parallel_tool_calls")
	}

	if req.StructuredOutput && !model.SupportsStructuredOutput {
		return domain.ErrUnsupportedFeature("structured_output")
	}

	if strings.EqualFold(model.ProviderConfig.ProviderName, "doubleword") && req.ServiceTier != nil {
		return domain.ErrInvalidField("service_tier is fixed by the selected Doubleword model")
	}

	if model.EffectiveProofMode() == domain.ProofModeTinfoilAttestedTransport &&
		!strings.EqualFold(model.ProviderConfig.ProviderName, "tinfoil") {
		return domain.ErrUnsupportedFeature("Tinfoil verified transport for non-Tinfoil provider")
	}

	return nil
}

func (s *GenerateService) recordFailure(ctx context.Context, reservationID *string, auth *domain.AuthContext, endpoint string, req *domain.GenerateRequest, model *domain.PublicModel, err error, partialUsage *domain.Usage, latencyMs int64) {
	if s.metering == nil {
		return
	}
	if reservationID != nil {
		// Once a hold exists, releasing it is accounting work rather than request
		// work and must not be canceled with the downstream connection.
		ctx = context.WithoutCancel(ctx)
	}
	if recErr := s.metering.RecordFailure(ctx, reservationID, auth, endpoint, req, model, err, partialUsage, latencyMs); recErr != nil && s.logger != nil {
		fields := []any{"error_type", fmt.Sprintf("%T", recErr)}
		if auth != nil {
			fields = append(fields, "account_id", auth.Account.ID)
		}
		if reservationID != nil {
			fields = append(fields, "reservation_id", *reservationID)
		}
		s.logger.Error("failed to record usage failure", fields...)
	}
}

// recordGeneration records a completed generation (success or failure) in the
// generation metrics, labeled by stream so that streaming and non-streaming
// distributions stay separate.
func (s *GenerateService) recordGeneration(outcome, endpoint string, req domain.GenerateRequest, model domain.PublicModel, latencyMs int64) {
	s.recordTerminalOutcome(outcome, endpoint, req, &model, latencyMs)
}

// recordTerminalOutcome records the terminal generation outcome for requests
// that fail before reaching the provider. It uses the resolved model when one
// exists; otherwise it records the request as unrouted so no terminal outcome
// is silently dropped.
func (s *GenerateService) recordTerminalOutcome(outcome, endpoint string, req domain.GenerateRequest, model *domain.PublicModel, latencyMs int64) {
	providerName := "unknown"
	if model != nil {
		providerName = model.ProviderConfig.ProviderName
	}
	stream := strconv.FormatBool(req.Stream)
	metrics.GenerationsTotal.WithLabelValues(
		endpoint,
		req.PublicModelID,
		providerName,
		stream,
		outcome,
	).Inc()
	metrics.GenerationDuration.WithLabelValues(endpoint, req.PublicModelID, providerName, stream).
		Observe(float64(latencyMs) / 1000.0)
}

// buildErrorLogFields builds a structured log field slice for error conditions,
// including request context, provider details, and numeric upstream status, never error messages or arbitrary metadata.
func (s *GenerateService) buildErrorLogFields(ctx context.Context, requestID string, authCtx *domain.AuthContext, endpoint, publicModelID string, model domain.PublicModel, err error, latencyMs int64) []any {
	fields := []any{
		"request_id", requestID,
		"endpoint", endpoint,
		"model", publicModelID,
		"provider", model.ProviderConfig.ProviderName,
		"provider_model", model.UpstreamModelName,
		"latency_ms", latencyMs,
		"error_type", fmt.Sprintf("%T", err),
	}
	if authCtx != nil {
		fields = append(fields, "account_id", authCtx.Account.ID)
	}
	// Merge structured upstream metadata from GatewayError if present.
	var gwErr *domain.GatewayError
	if errors.As(err, &gwErr) {
		fields = append(fields, "error_code", gwErr.Code)
		fields = append(fields, "gateway_status", gwErr.HTTPStatus)
		// Upstream messages and arbitrary metadata may echo request contents.
		if status, ok := gwErr.Metadata["upstream_status"].(int); ok {
			fields = append(fields, "upstream_status", status)
		}
	}
	return fields
}

func (s *GenerateService) storeTinfoilProof(ctx context.Context, auth domain.AuthContext, model domain.PublicModel, result domain.GenerateResult) {
	if s == nil || s.tinfoilProofs == nil || model.EffectiveProofMode() != domain.ProofModeTinfoilAttestedTransport {
		return
	}
	if result.TinfoilProof == nil {
		if s.logger != nil {
			s.logger.Warn("Tinfoil proof mode enabled but provider returned no proof evidence",
				"provider", model.ProviderConfig.ProviderName,
				"model", model.PublicModelID,
				"provider_response_id", result.ID,
			)
		}
		return
	}
	proof := *result.TinfoilProof
	proof.AccountID = auth.Account.ID
	proof.APIKeyID = auth.APIKey.ID
	proof.Provider = model.ProviderConfig.ProviderName
	proof.PublicModelID = model.PublicModelID
	proof.UpstreamModelID = model.UpstreamModelName
	if proof.ProviderResponseID == "" {
		proof.ProviderResponseID = result.ID
	}
	if proof.ProviderResponseID == "" {
		if s.logger != nil {
			s.logger.Error("failed to store Tinfoil proof: missing provider response id",
				"provider", model.ProviderConfig.ProviderName,
				"model", model.PublicModelID,
			)
		}
		return
	}
	if proof.CreatedAt.IsZero() {
		proof.CreatedAt = time.Now().UTC()
	}
	if err := s.tinfoilProofs.UpsertTinfoilTransportProof(ctx, proof); err != nil && s.logger != nil {
		s.logger.Error("failed to store Tinfoil transport proof",
			"provider", model.ProviderConfig.ProviderName,
			"model", model.PublicModelID,
			"provider_response_id", proof.ProviderResponseID,
			"error_type", fmt.Sprintf("%T", err),
		)
	}
}
