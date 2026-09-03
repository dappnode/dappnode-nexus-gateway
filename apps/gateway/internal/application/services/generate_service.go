package services

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/google/uuid"
)

// GenerateService is the single canonical execution path for all generation endpoints.
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

// Execute runs a non-streaming generation request through the canonical flow.
func (s *GenerateService) Execute(ctx context.Context, endpoint string, req domain.GenerateRequest, bearerToken string) (domain.GenerateResult, domain.AuthContext, error) {
	start := time.Now()
	requestID := middleware.GetRequestID(ctx)

	authCtx, err := s.auth.AuthenticateAPIKey(ctx, bearerToken)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, req, nil, time.Since(start).Milliseconds())
		return domain.GenerateResult{}, domain.AuthContext{}, err
	}

	model, execReq, err := s.resolveModel(ctx, req)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, req, nil, time.Since(start).Milliseconds())
		return domain.GenerateResult{}, authCtx, err
	}

	if err := s.validateRequest(endpoint, execReq, model); err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, execReq, &model, time.Since(start).Milliseconds())
		s.recordFailure(ctx, nil, &authCtx, endpoint, &execReq, &model, err, nil, time.Since(start).Milliseconds())
		return domain.GenerateResult{}, authCtx, err
	}

	piiMapping, err := s.maskRequest(ctx, &execReq, authCtx.APIKey.PIIMode)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, execReq, &model, time.Since(start).Milliseconds())
		s.recordFailure(ctx, nil, &authCtx, endpoint, &execReq, &model, err, nil, time.Since(start).Milliseconds())
		return domain.GenerateResult{}, authCtx, err
	}

	reservationRequestID := uuid.NewString()
	reservationID, err := s.metering.Reserve(ctx, authCtx, endpoint, execReq, model, reservationRequestID)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, execReq, &model, time.Since(start).Milliseconds())
		return domain.GenerateResult{}, authCtx, err
	}

	result, executionModel, err := s.generateWithFallback(ctx, execReq, model, requestID, piiMapping)
	if err != nil {
		err = sanitizeErrorWithPIIMapping(err, piiMapping)
		fields := s.buildErrorLogFields(ctx, requestID, &authCtx, endpoint, execReq.PublicModelID, executionModel, err, time.Since(start).Milliseconds())
		s.logger.Error("provider generation failed", fields...)
		s.recordGeneration(metrics.OutcomeError, endpoint, execReq, executionModel, time.Since(start).Milliseconds())
		s.recordFailure(ctx, &reservationID, &authCtx, endpoint, &execReq, &executionModel, err, nil, time.Since(start).Milliseconds())
		return domain.GenerateResult{}, authCtx, err
	}

	s.storeTinfoilProof(ctx, authCtx, executionModel, result)

	latencyMs := time.Since(start).Milliseconds()
	metrics.RecordUsage(result.Usage, execReq.PublicModelID, executionModel.ProviderConfig.ProviderName)
	s.recordGeneration(metrics.OutcomeSuccess, endpoint, execReq, executionModel, latencyMs)
	s.logger.Info("generation completed",
		"request_id", requestID,
		"account_id", authCtx.Account.ID,
		"endpoint", endpoint,
		"model", execReq.PublicModelID,
		"provider", executionModel.ProviderConfig.ProviderName,
		"latency_ms", latencyMs,
		"finish_reason", result.FinishReason,
		"gateway_status", 200,
		"upstream_status", 200,
	)

	// Provider execution may finish at the same moment the client disconnects.
	// Finalizing the reservation must outlive request cancellation so a
	// successful upstream call is still charged and its hold is released.
	if err := s.metering.RecordSuccess(context.WithoutCancel(ctx), reservationID, authCtx, endpoint, execReq, result, executionModel, latencyMs); err != nil {
		s.logger.Error("failed to record usage",
			"request_id", requestID,
			"reservation_id", reservationID,
			"account_id", authCtx.Account.ID,
			"error", err,
		)
	}

	unmaskResult(&result, piiMapping, s.logger,
		"request_id", requestID,
		"endpoint", endpoint,
		"model", execReq.PublicModelID,
		"provider", executionModel.ProviderConfig.ProviderName,
		"pii_mode", authCtx.APIKey.PIIMode,
	)
	return result, authCtx, nil
}

// ExecuteStream runs a streaming generation request through the canonical flow.
func (s *GenerateService) ExecuteStream(ctx context.Context, endpoint string, req domain.GenerateRequest, bearerToken string) (ports.GenerationStream, domain.AuthContext, *domain.PublicModel, error) {
	start := time.Now()
	requestID := middleware.GetRequestID(ctx)

	authCtx, err := s.auth.AuthenticateAPIKey(ctx, bearerToken)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, req, nil, time.Since(start).Milliseconds())
		return nil, domain.AuthContext{}, nil, err
	}

	model, execReq, err := s.resolveModel(ctx, req)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, req, nil, time.Since(start).Milliseconds())
		return nil, authCtx, nil, err
	}

	if err := s.validateRequest(endpoint, execReq, model); err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, execReq, &model, time.Since(start).Milliseconds())
		s.recordFailure(ctx, nil, &authCtx, endpoint, &execReq, &model, err, nil, time.Since(start).Milliseconds())
		return nil, authCtx, &model, err
	}

	piiMapping, err := s.maskRequest(ctx, &execReq, authCtx.APIKey.PIIMode)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, execReq, &model, time.Since(start).Milliseconds())
		s.recordFailure(ctx, nil, &authCtx, endpoint, &execReq, &model, err, nil, time.Since(start).Milliseconds())
		return nil, authCtx, &model, err
	}

	reservationRequestID := uuid.NewString()
	reservationID, err := s.metering.Reserve(ctx, authCtx, endpoint, execReq, model, reservationRequestID)
	if err != nil {
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, execReq, &model, time.Since(start).Milliseconds())
		return nil, authCtx, &model, err
	}

	stream, executionModel, err := s.streamWithFallback(ctx, execReq, model, requestID, piiMapping)
	if err != nil {
		err = sanitizeErrorWithPIIMapping(err, piiMapping)
		fields := s.buildErrorLogFields(ctx, requestID, &authCtx, endpoint, execReq.PublicModelID, executionModel, err, time.Since(start).Milliseconds())
		s.logger.Error("provider stream generation failed", fields...)
		s.recordGeneration(metrics.OutcomeError, endpoint, execReq, executionModel, time.Since(start).Milliseconds())
		s.recordFailure(ctx, &reservationID, &authCtx, endpoint, &execReq, &executionModel, err, nil, time.Since(start).Milliseconds())
		return nil, authCtx, &executionModel, err
	}

	wrapped := &usageTrackingStream{
		inner:         stream,
		service:       s,
		ctx:           ctx,
		authCtx:       authCtx,
		endpoint:      endpoint,
		req:           execReq,
		model:         executionModel,
		requestID:     requestID,
		reservationID: reservationID,
		start:         start,
		piiMapping:    piiMapping,
	}

	if piiMapping != nil && piiMapping.Len() > 0 {
		return newPIIUnmaskingStreamWithLogger(wrapped, piiMapping, s.logger,
			"request_id", requestID,
			"endpoint", endpoint,
			"model", execReq.PublicModelID,
			"provider", executionModel.ProviderConfig.ProviderName,
			"pii_mode", authCtx.APIKey.PIIMode,
		), authCtx, &executionModel, nil
	}
	return wrapped, authCtx, &executionModel, nil
}

func (s *GenerateService) generateWithFallback(
	ctx context.Context,
	req domain.GenerateRequest,
	model domain.PublicModel,
	requestID string,
	piiMapping *domain.PIIMapping,
) (domain.GenerateResult, domain.PublicModel, error) {
	// ponytail: one fallback attempt is intentional; add chains only when a real routing need appears.
	result, err := s.generateOnce(ctx, req, model)
	if err == nil || !shouldTryFallback(ctx, err, model.Fallback) {
		return result, model, err
	}

	fallback := withProviderTarget(model, *model.Fallback)
	s.logFallback(requestID, model, fallback, sanitizeErrorWithPIIMapping(err, piiMapping))
	result, err = s.generateOnce(ctx, req, fallback)
	return result, fallback, err
}

func (s *GenerateService) generateOnce(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (domain.GenerateResult, error) {
	provider, err := s.registry.GetProvider(model.ProviderConfig.ProviderName)
	if err != nil {
		return domain.GenerateResult{}, err
	}
	upstreamStart := time.Now()
	result, err := provider.Generate(ctx, req, model)
	recordUpstreamLatency(req.PublicModelID, model.ProviderConfig.ProviderName, upstreamStart, err)
	return result, err
}

func (s *GenerateService) streamWithFallback(
	ctx context.Context,
	req domain.GenerateRequest,
	model domain.PublicModel,
	requestID string,
	piiMapping *domain.PIIMapping,
) (ports.GenerationStream, domain.PublicModel, error) {
	stream, err := s.streamOnce(ctx, req, model)
	if err == nil {
		stream, err = primeStream(stream)
	}
	if err == nil || !shouldTryFallback(ctx, err, model.Fallback) {
		return stream, model, err
	}

	fallback := withProviderTarget(model, *model.Fallback)
	s.logFallback(requestID, model, fallback, sanitizeErrorWithPIIMapping(err, piiMapping))
	stream, err = s.streamOnce(ctx, req, fallback)
	if err == nil {
		stream, err = primeStream(stream)
	}
	return stream, fallback, err
}

func (s *GenerateService) streamOnce(ctx context.Context, req domain.GenerateRequest, model domain.PublicModel) (ports.GenerationStream, error) {
	provider, err := s.registry.GetProvider(model.ProviderConfig.ProviderName)
	if err != nil {
		return nil, err
	}
	upstreamStart := time.Now()
	stream, err := provider.StreamGenerate(ctx, req, model)
	recordUpstreamLatency(req.PublicModelID, model.ProviderConfig.ProviderName, upstreamStart, err)
	return stream, err
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
		"error", err,
	)
}

type bufferedGenerationStream struct {
	inner  ports.GenerationStream
	events []domain.StreamEvent
}

func primeStream(stream ports.GenerationStream) (ports.GenerationStream, error) {
	buffered := &bufferedGenerationStream{inner: stream}
	for {
		event, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return buffered, nil
			}
			_ = stream.Close()
			return nil, err
		}
		if event.Type == domain.StreamEventError {
			_ = stream.Close()
			if event.Error != nil {
				return nil, event.Error
			}
			return nil, domain.ErrProviderError(502, "provider stream failed before output")
		}
		buffered.events = append(buffered.events, event)
		if streamEventCommitsResponse(event) {
			return buffered, nil
		}
	}
}

func streamEventCommitsResponse(event domain.StreamEvent) bool {
	return event.Type == domain.StreamEventCompleted ||
		(event.ContentDelta != nil && *event.ContentDelta != "") ||
		(event.ReasoningDelta != nil && *event.ReasoningDelta != "") ||
		event.ToolCallDelta != nil
}

func (s *bufferedGenerationStream) Recv() (domain.StreamEvent, error) {
	if len(s.events) == 0 {
		return s.inner.Recv()
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *bufferedGenerationStream) Close() error {
	return s.inner.Close()
}

func (s *bufferedGenerationStream) VerifiedTransportProof() *domain.TinfoilTransportProof {
	if proof, ok := s.inner.(ports.VerifiedTransportProofProvider); ok {
		return proof.VerifiedTransportProof()
	}
	return nil
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

	if req.TextConfig != nil && !model.SupportsStructuredOutput {
		return domain.ErrUnsupportedFeature("structured_output")
	}

	if model.EffectiveProofMode() == domain.ProofModeTinfoilAttestedTransport &&
		!strings.EqualFold(model.ProviderConfig.ProviderName, "tinfoil") {
		return domain.ErrUnsupportedFeature("Tinfoil verified transport for non-Tinfoil provider")
	}

	return nil
}

// usageTrackingStream wraps a GenerationStream to record usage on completion.
type usageTrackingStream struct {
	inner              ports.GenerationStream
	service            *GenerateService
	ctx                context.Context
	authCtx            domain.AuthContext
	endpoint           string
	req                domain.GenerateRequest
	model              domain.PublicModel
	requestID          string
	reservationID      string
	start              time.Time
	piiMapping         *domain.PIIMapping
	lastUsage          *domain.Usage
	finishReason       *string
	providerResponseID string
	finished           bool
}

func (s *usageTrackingStream) Recv() (domain.StreamEvent, error) {
	event, err := s.inner.Recv()
	if err != nil {
		if err == io.EOF {
			s.recordCompletion(nil)
			return event, err
		}
		// If the model already sent a finish reason, treat post-completion
		// errors (e.g. context canceled after client disconnect) as success.
		if s.finishReason != nil {
			s.recordCompletion(nil)
			return event, io.EOF
		}
		s.recordCompletion(err)
		return event, err
	}

	if event.Usage != nil {
		s.lastUsage = event.Usage
	}
	if event.ProviderResponseID != "" {
		s.providerResponseID = event.ProviderResponseID
	}

	if event.Type == domain.StreamEventCompleted {
		if event.FinishReason != nil {
			s.finishReason = event.FinishReason
		}
	}

	if event.Type == domain.StreamEventError {
		var gwErr error
		if event.Error != nil {
			gwErr = event.Error
		}
		s.recordCompletion(gwErr)
	}

	return event, nil
}

func (s *usageTrackingStream) Close() error {
	if !s.finished {
		s.recordCompletion(context.Canceled)
	}
	return s.inner.Close()
}

func (s *usageTrackingStream) recordCompletion(err error) {
	if s.finished {
		return
	}
	s.finished = true
	ctx := context.WithoutCancel(s.ctx)
	latencyMs := time.Since(s.start).Milliseconds()
	if err != nil {
		err = sanitizeErrorWithPIIMapping(err, s.piiMapping)
		fields := s.service.buildErrorLogFields(ctx, s.requestID, &s.authCtx, s.endpoint, s.req.PublicModelID, s.model, err, latencyMs)
		s.service.logger.Error("stream error", fields...)
		s.service.recordGeneration(metrics.OutcomeError, s.endpoint, s.req, s.model, latencyMs)
		s.service.recordFailure(ctx, &s.reservationID, &s.authCtx, s.endpoint, &s.req, &s.model, err, s.lastUsage, latencyMs)
		return
	}
	// Stream ended normally (io.EOF) — record success with accumulated usage.
	result := domain.GenerateResult{
		ID:              s.providerResponseID,
		PublicModelID:   s.model.PublicModelID,
		ProviderName:    s.model.ProviderConfig.ProviderName,
		ProviderModelID: s.model.ProviderModelID,
		FinishReason:    s.finishReason,
		Usage:           s.lastUsage,
	}
	if verified, ok := s.inner.(ports.VerifiedTransportProofProvider); ok {
		result.TinfoilProof = verified.VerifiedTransportProof()
	}
	s.service.storeTinfoilProof(ctx, s.authCtx, s.model, result)
	metrics.RecordUsage(s.lastUsage, s.req.PublicModelID, s.model.ProviderConfig.ProviderName)
	s.service.recordGeneration(metrics.OutcomeSuccess, s.endpoint, s.req, s.model, latencyMs)
	s.service.logger.Info("generation completed",
		"request_id", s.requestID,
		"account_id", s.authCtx.Account.ID,
		"endpoint", s.endpoint,
		"model", s.req.PublicModelID,
		"provider", s.model.ProviderConfig.ProviderName,
		"latency_ms", latencyMs,
		"finish_reason", s.finishReason,
		"gateway_status", 200,
		"upstream_status", 200,
	)
	if recErr := s.service.metering.RecordSuccess(ctx, s.reservationID, s.authCtx, s.endpoint, s.req, result, s.model, latencyMs); recErr != nil {
		s.service.logger.Error("failed to record stream usage",
			"request_id", s.requestID,
			"reservation_id", s.reservationID,
			"account_id", s.authCtx.Account.ID,
			"error", recErr,
		)
	}
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
		fields := []any{"error", recErr}
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
// including request context, provider details, and any upstream error metadata.
func (s *GenerateService) buildErrorLogFields(ctx context.Context, requestID string, authCtx *domain.AuthContext, endpoint, publicModelID string, model domain.PublicModel, err error, latencyMs int64) []any {
	fields := []any{
		"request_id", requestID,
		"endpoint", endpoint,
		"model", publicModelID,
		"provider", model.ProviderConfig.ProviderName,
		"provider_model", model.UpstreamModelName,
		"latency_ms", latencyMs,
		"error", err.Error(),
	}
	if authCtx != nil {
		fields = append(fields, "account_id", authCtx.Account.ID)
	}
	// Merge structured upstream metadata from GatewayError if present.
	var gwErr *domain.GatewayError
	if errors.As(err, &gwErr) {
		fields = append(fields, "error_code", gwErr.Code)
		fields = append(fields, "gateway_status", gwErr.HTTPStatus)
		fields = append(fields, gwErr.LogFields()...)
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
			"error", err,
		)
	}
}
