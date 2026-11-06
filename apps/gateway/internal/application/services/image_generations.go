package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/google/uuid"
)

// Image generation defaults; see SetImageOptions.
const (
	// DefaultImageTimeout bounds one image generation, fallback included.
	DefaultImageTimeout = 120 * time.Second
	// DefaultImageConcurrency is how many image generations one account
	// runs at once.
	DefaultImageConcurrency = 2
)

// ImageCall is a finished image generation: the provider's JSON, without the
// provider's own usage and cost.
type ImageCall struct {
	Model domain.PublicModel
	Body  []byte
	// Replayed is set when the body is the stored result of an earlier
	// request with the same Idempotency-Key; nothing was charged again.
	Replayed bool
}

// SetImageOptions sets the image generation timeout and how many image
// generations one account may run at once.
func (s *GenerateService) SetImageOptions(timeout time.Duration, perAccount int) {
	if timeout > 0 {
		s.imageTimeout = timeout
	}
	if perAccount > 0 {
		s.imageJobs = newAccountSlots(perAccount)
	}
}

// GenerateImages runs POST /v1/images/generations. It authenticates, keeps
// guests out, holds the price of the requested images, calls the provider
// (falling back like chat), and charges the images the provider returned. A
// request the provider refuses costs nothing.
//
// idempotencyKey, when set, makes retries safe: a retry of a finished request
// gets the same images back without a second charge, and a retry of a
// running one is refused until it finishes.
func (s *GenerateService) GenerateImages(ctx context.Context, raw []byte, req domain.GenerateRequest, bearerToken, idempotencyKey string) (*ImageCall, error) {
	const endpoint = domain.EndpointImageGenerations
	start := time.Now()
	requestID := middleware.GetRequestID(ctx)
	fail := func(err error, model *domain.PublicModel) error {
		err = clientGone(ctx, err)
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, req, model, time.Since(start).Milliseconds())
		s.recordError(endpoint, req, model, metrics.PhaseRequest, err)
		return err
	}

	authCtx, err := s.auth.AuthenticateAPIKey(ctx, bearerToken)
	if err != nil {
		return nil, fail(err, nil)
	}
	reject := func(err error, model *domain.PublicModel) error {
		err = fail(err, model)
		if model == nil {
			return domain.Annotate(err, "account_id", authCtx.Account.ID, "requested_model", req.PublicModelID)
		}
		return domain.Annotate(err, "account_id", authCtx.Account.ID, "model", model.PublicModelID, "provider", model.ProviderConfig.ProviderName)
	}
	// Guests never get media.
	if authCtx.Account.IsGuest {
		return nil, reject(domain.ErrGuestModelNotAllowed(req.PublicModelID), nil)
	}
	model, err := s.catalog.GetPublicModel(ctx, req.PublicModelID)
	if err != nil {
		return nil, reject(err, nil)
	}
	if err := s.validateRequest(endpoint, req, model); err != nil {
		s.recordFailure(ctx, nil, &authCtx, endpoint, &req, &model, err, nil, time.Since(start).Milliseconds())
		return nil, reject(err, &model)
	}

	var idem *idempotentRequest
	if idempotencyKey != "" {
		var replay []byte
		idem, replay, err = s.idempotency.begin(authCtx.Account.ID, idempotencyKey, raw)
		if err != nil {
			return nil, reject(err, &model)
		}
		if replay != nil {
			s.logger.Info("image generation replayed", "request_id", requestID, "account_id", authCtx.Account.ID, "model", model.PublicModelID)
			return &ImageCall{Model: model, Body: replay, Replayed: true}, nil
		}
	}
	call, err := s.generateImages(ctx, authCtx, raw, req, model, start)
	if idem != nil {
		if err != nil {
			s.idempotency.abandon(idem) // Nothing was charged: the key may be retried.
		} else {
			s.idempotency.finish(idem, call.Body)
		}
	}
	return call, err
}

func (s *GenerateService) generateImages(ctx context.Context, authCtx domain.AuthContext, raw []byte, req domain.GenerateRequest, model domain.PublicModel, start time.Time) (*ImageCall, error) {
	const endpoint = domain.EndpointImageGenerations
	requestID := middleware.GetRequestID(ctx)
	reject := func(err error) error {
		err = clientGone(ctx, err)
		s.recordTerminalOutcome(metrics.OutcomeError, endpoint, req, &model, time.Since(start).Milliseconds())
		s.recordError(endpoint, req, &model, metrics.PhaseRequest, err)
		return domain.Annotate(err, "account_id", authCtx.Account.ID, "model", model.PublicModelID, "provider", model.ProviderConfig.ProviderName)
	}

	release, ok := s.imageJobs.acquire(authCtx.Account.ID)
	if !ok {
		return nil, reject(domain.ErrTooManyConcurrent(s.imageJobs.limit))
	}
	defer release()

	masked, _, err := s.maskBody(ctx, raw, authCtx.APIKey.PIIMode, endpoint)
	if err != nil {
		s.recordFailure(ctx, nil, &authCtx, endpoint, &req, &model, err, nil, time.Since(start).Milliseconds())
		return nil, reject(err)
	}
	reservationID, err := s.metering.Reserve(ctx, authCtx, endpoint, req, model, uuid.NewString())
	if err != nil {
		return nil, reject(err)
	}

	// From here the provider runs and bills us: like chat, the generation no
	// longer follows the client's connection, so a client that hangs up is
	// still charged for the images it asked for, and gets them on a retry
	// with the same Idempotency-Key.
	clientCtx := ctx
	ctx, cancelTimeout := context.WithTimeout(context.WithoutCancel(ctx), s.imageTimeout)
	defer cancelTimeout()
	ctx, cancelInference := context.WithCancelCause(ctx)
	defer cancelInference(nil)
	stopRenewal := func() {}
	if keeper, ok := s.metering.(ports.ReservationKeeper); ok {
		stopRenewal = keeper.KeepReservation(ctx, reservationID, func() {
			cancelInference(domain.ErrInternal("credit reservation could not be maintained").WithMeta("dependency", "metering", "cause", domain.CauseCreditHoldLost))
		})
	}
	defer stopRenewal()

	attempt := func(target domain.PublicModel) ([]byte, int64, error) {
		if err := s.validateRequest(endpoint, req, target); err != nil {
			return nil, 0, err
		}
		provider, err := s.registry.GetProvider(target.ProviderConfig.ProviderName)
		if err != nil {
			return nil, 0, domain.ErrProviderUnavailable(target.ProviderConfig.ProviderName).WithMeta(
				"fault", domain.FaultNexus, "cause", domain.CauseProviderNotRegistered)
		}
		images, ok := provider.(ports.ImageProvider)
		if !ok {
			return nil, 0, domain.ErrUnsupportedEndpoint(target.PublicModelID, endpoint).WithMeta(
				"fault", domain.FaultNexus, "provider", target.ProviderConfig.ProviderName)
		}
		upstreamStart := time.Now()
		body, err := images.GenerateImages(ctx, masked, target)
		recordUpstreamLatency(req.PublicModelID, target.ProviderConfig.ProviderName, upstreamStart, err)
		if err != nil {
			return nil, 0, imageProviderError(err)
		}
		return readImageResponse(body, req.Images)
	}

	executed := model
	body, count, err := attempt(model)
	if err != nil && clientCtx.Err() == nil && shouldTryFallback(ctx, err, model.Fallback) &&
		withProviderTarget(model, *model.Fallback).SupportsEndpoint(endpoint) {
		executed = withProviderTarget(model, *model.Fallback)
		s.logFallback(requestID, model, executed, err)
		metrics.RecordFallback(endpoint, model.PublicModelID, model.ProviderConfig.ProviderName, executed.ProviderConfig.ProviderName, err)
		body, count, err = attempt(executed)
	}
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		if cause, ok := context.Cause(ctx).(*domain.GatewayError); ok {
			err = cause
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = domain.ErrProviderTimeout(executed.ProviderConfig.ProviderName).WithMeta("timeout_ms", s.imageTimeout.Milliseconds())
		}
		s.logger.Error("provider request failed", s.buildErrorLogFields(ctx, requestID, &authCtx, endpoint, req.PublicModelID, executed, err, latencyMs)...)
		s.recordGeneration(metrics.OutcomeError, endpoint, req, executed, latencyMs)
		s.recordError(endpoint, req, &executed, metrics.PhaseProvider, err)
		// No usage: a failed or refused generation is not charged.
		s.recordFailure(ctx, &reservationID, &authCtx, endpoint, &req, &executed, err, nil, latencyMs)
		return nil, err
	}

	usage := &domain.Usage{Images: count}
	result := domain.GenerateResult{
		PublicModelID: executed.PublicModelID, ProviderName: executed.ProviderConfig.ProviderName,
		ProviderModelID: executed.ProviderModelID, Usage: usage,
	}
	metrics.RecordUsage(usage, req.PublicModelID, executed.ProviderConfig.ProviderName)
	s.recordGeneration(metrics.OutcomeSuccess, endpoint, req, executed, latencyMs)
	s.logger.Info("generation completed",
		"request_id", requestID, "reservation_id", reservationID, "account_id", authCtx.Account.ID,
		"endpoint", endpoint, "model", req.PublicModelID, "provider", executed.ProviderConfig.ProviderName,
		"provider_model", executed.UpstreamModelName, "images_requested", req.Images, "images", count,
		"latency_ms", latencyMs, "gateway_status", 200, "upstream_status", 200)
	if err := s.metering.RecordSuccess(context.WithoutCancel(ctx), reservationID, authCtx, endpoint, req, result, executed, latencyMs); err != nil {
		s.logger.Error("failed to record usage", "request_id", requestID, "reservation_id", reservationID, "error_type", fmt.Sprintf("%T", err))
	}
	return &ImageCall{Model: executed, Body: body}, nil
}

// readImageResponse checks a provider's image response and counts its
// images. Every image must be inline: the gateway stores no media and never
// hands out a provider's URL. The provider's usage and cost are removed;
// what the account pays is Nexus's price.
func readImageResponse(body []byte, requested *int) ([]byte, int64, error) {
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, 0, domain.ErrProviderError(http.StatusBadGateway, "provider returned an invalid response")
	}
	var data []struct {
		B64JSON string `json:"b64_json"`
	}
	if err := json.Unmarshal(resp["data"], &data); err != nil || len(data) == 0 {
		return nil, 0, domain.ErrProviderError(http.StatusBadGateway, "provider returned no images")
	}
	for _, item := range data {
		if item.B64JSON == "" {
			return nil, 0, domain.ErrProviderError(http.StatusBadGateway, "provider returned an image that is not inline")
		}
	}
	if requested != nil && len(data) > *requested {
		return nil, 0, domain.ErrProviderError(http.StatusBadGateway, "provider returned more images than requested")
	}
	delete(resp, "usage")
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, 0, domain.ErrInternal("an internal error occurred").WithMeta("reason", "re-encode image response")
	}
	return out, int64(len(data)), nil
}

// imageProviderError turns a provider's content-policy refusal into a 422
// the client can act on. Providers name it differently: OpenAI says
// moderation_blocked or content_policy_violation; others say safety, nsfw or
// content filter.
func imageProviderError(err error) error {
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) {
		return err
	}
	status, _ := gwErr.Metadata["upstream_status"].(int)
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return err
	}
	var text strings.Builder
	for _, key := range []string{"upstream_code", "upstream_type", "upstream_reason", "upstream_error"} {
		if v, ok := gwErr.Metadata[key].(string); ok {
			text.WriteString(strings.ToLower(v))
			text.WriteByte(' ')
		}
	}
	for _, marker := range []string{"moderation", "content_policy", "content policy", "content_filter", "content filter", "safety", "nsfw"} {
		if strings.Contains(text.String(), marker) {
			refusal := domain.ErrContentPolicy()
			refusal.Metadata = gwErr.Metadata
			return refusal
		}
	}
	return err
}
