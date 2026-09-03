package metering

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	enclavenetwork "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/network"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	enclavenetwork.ConfigureMeteringTransport(transport)
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: timeout, Transport: transport},
	}
}

func (c *Client) Reserve(ctx context.Context, auth domain.AuthContext, endpoint string, req domain.GenerateRequest, model domain.PublicModel, reservationRequestID string) (string, error) {
	payload := reserveRequest{
		GatewayRequestID:       reservationRequestID,
		AccountID:              auth.Account.ID,
		APIKeyID:               auth.APIKey.ID,
		Endpoint:               endpoint,
		PublicModelID:          model.PublicModelID,
		RequestedPublicModelID: requestedModelID(req),
		RouterID:               req.RouterID,
		RoutedPublicModelID:    req.RoutedPublicModelID,
		MatchedCategory:        req.MatchedCategory,
		RoutingScore:           req.RoutingScore,
		RoutingCategoryScores:  req.RoutingCategoryScores,
		DecisionReason:         req.DecisionReason,
		FallbackUsed:           req.FallbackUsed,
		Stream:                 req.Stream,
		MaxOutputTokens:        req.MaxOutputTokens,
	}
	var res reserveResponse
	if err := c.postJSON(ctx, "/internal/metering/reservations", payload, &res, 2); err != nil {
		return "", err
	}
	if res.ReservationID == "" {
		return "", meteringInternalError("reason", "missing reservation_id")
	}
	return res.ReservationID, nil
}

func (c *Client) RecordSuccess(ctx context.Context, reservationID string, _ domain.AuthContext, _ string, _ domain.GenerateRequest, resp domain.GenerateResult, model domain.PublicModel, latencyMs int64) error {
	providerName := resp.ProviderName
	if providerName == "" {
		providerName = model.ProviderConfig.ProviderName
	}
	providerModelID := resp.ProviderModelID
	if providerModelID == "" {
		providerModelID = model.ProviderModelID
	}
	payload := completeRequest{
		ProviderName:      providerName,
		ProviderModelID:   providerModelID,
		ProviderRequestID: resp.ID,
		FinishReason:      resp.FinishReason,
		Usage:             resp.Usage,
		LatencyMs:         latencyMs,
	}
	var res completeResponse
	return c.postJSON(ctx, "/internal/metering/reservations/"+reservationID+"/complete", payload, &res, 2)
}

func (c *Client) RecordFailure(ctx context.Context, reservationID *string, auth *domain.AuthContext, endpoint string, req *domain.GenerateRequest, model *domain.PublicModel, err error, partialUsage *domain.Usage, latencyMs int64) error {
	payload := failureRequest{
		ReservationID: reservationIDValue(reservationID),
		Endpoint:      endpoint,
		ErrorType:     domain.ErrTypeInternal,
		ErrorCode:     domain.ErrCodeInternalError,
		PartialUsage:  partialUsage,
		LatencyMs:     latencyMs,
	}
	if auth != nil {
		payload.AccountID = auth.Account.ID
		payload.APIKeyID = auth.APIKey.ID
	}
	if req != nil {
		payload.PublicModelID = req.PublicModelID
		payload.RequestedPublicModelID = requestedModelID(*req)
		payload.RouterID = req.RouterID
		payload.RoutedPublicModelID = req.RoutedPublicModelID
		payload.MatchedCategory = req.MatchedCategory
		payload.RoutingScore = req.RoutingScore
		payload.RoutingCategoryScores = req.RoutingCategoryScores
		payload.DecisionReason = req.DecisionReason
		payload.FallbackUsed = req.FallbackUsed
		payload.Stream = req.Stream
	}
	if model != nil {
		payload.PublicModelID = model.PublicModelID
		payload.ProviderName = model.ProviderConfig.ProviderName
		payload.ProviderModelID = model.ProviderModelID
	}
	var gwErr *domain.GatewayError
	if errors.As(err, &gwErr) {
		payload.ErrorType = gwErr.Type
		payload.ErrorCode = gwErr.Code
	}
	retries := 0
	if reservationID != nil {
		retries = 2
	}
	return c.postJSON(ctx, "/internal/metering/usage-failures", payload, nil, retries)
}

func (c *Client) postJSON(ctx context.Context, path string, payload any, out any, retries int) error {
	return c.requestJSON(ctx, http.MethodPost, path, payload, out, retries)
}

func (c *Client) getJSON(ctx context.Context, path string, out any, retries int) error {
	return c.requestJSON(ctx, http.MethodGet, path, nil, out, retries)
}

func (c *Client) requestJSON(ctx context.Context, method, path string, payload any, out any, retries int) error {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt*150) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := c.requestJSONOnce(ctx, method, path, payload, out); err != nil {
			lastErr = err
			if !isRetryableRequestError(err) {
				return err
			}
			continue
		}
		return nil
	}
	return unwrapRetryableRequestError(lastErr)
}

func (c *Client) requestJSONOnce(ctx context.Context, method, path string, payload any, out any) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	op := meteringOperation(method, path)
	start := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		metrics.MeteringRequests.WithLabelValues(op, "transport_error").Inc()
		metrics.MeteringDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return newRetryableRequestError(meteringInternalError("reason", err.Error()))
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		metrics.MeteringRequests.WithLabelValues(op, strconv.Itoa(res.StatusCode)).Inc()
		metrics.MeteringDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
		var envelope errorResponse
		_ = json.NewDecoder(res.Body).Decode(&envelope)
		if res.StatusCode >= 500 {
			return newRetryableRequestError(meteringResponseError(res.StatusCode, envelope.Error.Code))
		}
		if clientErr := userFacingMeteringError(method, path, res.StatusCode, envelope); clientErr != nil {
			return clientErr
		}
		return meteringResponseError(res.StatusCode, envelope.Error.Code)
	}
	if out != nil {
		if res.StatusCode == http.StatusNoContent {
			metrics.MeteringRequests.WithLabelValues(op, "protocol_error").Inc()
			metrics.MeteringDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
			return newRetryableRequestError(
				meteringInternalError("reason", "missing response body"),
			)
		}
		if err := decodeJSONResponse(res.Body, out); err != nil {
			metrics.MeteringRequests.WithLabelValues(op, "protocol_error").Inc()
			metrics.MeteringDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
			return newRetryableRequestError(
				meteringInternalError("reason", "invalid response body: "+err.Error()),
			)
		}
	}
	metrics.MeteringRequests.WithLabelValues(op, "success").Inc()
	metrics.MeteringDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
	return nil
}

// decodeJSONResponse assigns to out only after one complete JSON value has
// decoded successfully. This prevents a failed attempt from leaving partial
// fields behind for a later retry.
func decodeJSONResponse(body io.Reader, out any) error {
	target := reflect.ValueOf(out)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return errors.New("response output must be a non-nil pointer")
	}

	decoder := json.NewDecoder(body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response body contains multiple JSON values")
		}
		return err
	}

	fresh := reflect.New(target.Elem().Type())
	if err := json.Unmarshal(raw, fresh.Interface()); err != nil {
		return err
	}
	target.Elem().Set(fresh.Elem())
	return nil
}

func userFacingMeteringError(method, path string, status int, envelope errorResponse) *domain.GatewayError {
	requestPath, _, _ := strings.Cut(path, "?")
	wantStatus := 0
	wantType := ""

	switch {
	case method == http.MethodPost && requestPath == "/internal/gateway/authenticate":
		switch envelope.Error.Code {
		case domain.ErrCodeInvalidAPIKey, domain.ErrCodeInactiveAPIKey:
			wantStatus = http.StatusUnauthorized
			wantType = domain.ErrTypeAuthentication
		case domain.ErrCodeInactiveAccount:
			wantStatus = http.StatusForbidden
			wantType = domain.ErrTypePermission
		}
	case method == http.MethodGet && strings.HasPrefix(requestPath, "/internal/gateway/models/"):
		if envelope.Error.Code == domain.ErrCodeUnsupportedModel {
			wantStatus = http.StatusNotFound
			wantType = domain.ErrTypeInvalidRequest
		}
	case method == http.MethodGet && strings.HasPrefix(requestPath, "/internal/gateway/routers/"):
		if envelope.Error.Code == domain.ErrCodeNotFound {
			wantStatus = http.StatusNotFound
			wantType = domain.ErrTypeInvalidRequest
		}
	case method == http.MethodGet && requestPath == "/internal/gateway/tinfoil-proofs":
		if envelope.Error.Code == domain.ErrCodeNotFound {
			wantStatus = http.StatusNotFound
			wantType = domain.ErrTypeInvalidRequest
		}
	case method == http.MethodPost && requestPath == "/internal/metering/reservations":
		if envelope.Error.Code == domain.ErrCodeInsufficientBalance {
			wantStatus = http.StatusPaymentRequired
			wantType = domain.ErrTypePermission
		}
	}

	if status != wantStatus || envelope.Error.Type != wantType || envelope.Error.Message == "" {
		return nil
	}
	return &domain.GatewayError{
		HTTPStatus: status,
		Type:       envelope.Error.Type,
		Code:       envelope.Error.Code,
		Message:    envelope.Error.Message,
	}
}

func meteringResponseError(status int, code string) *domain.GatewayError {
	err := meteringInternalError("metering_status", status)
	if code != "" {
		err = err.WithMeta("metering_code", code)
	}
	return err
}

func meteringInternalError(fields ...any) *domain.GatewayError {
	metadata := []any{"dependency", "metering"}
	metadata = append(metadata, fields...)
	return domain.ErrInternal("an internal error occurred").WithMeta(metadata...)
}

type retryableRequestError struct {
	err error
}

func newRetryableRequestError(err error) error {
	return &retryableRequestError{err: err}
}

func (e *retryableRequestError) Error() string {
	return e.err.Error()
}

func (e *retryableRequestError) Unwrap() error {
	return e.err
}

func isRetryableRequestError(err error) bool {
	var retryableErr *retryableRequestError
	return errors.As(err, &retryableErr)
}

func unwrapRetryableRequestError(err error) error {
	var retryableErr *retryableRequestError
	if errors.As(err, &retryableErr) {
		return retryableErr.err
	}
	return err
}

func requestedModelID(req domain.GenerateRequest) string {
	if req.RequestedModelID != "" {
		return req.RequestedModelID
	}
	return req.PublicModelID
}

// meteringOperation derives a stable, low-cardinality operation label for the
// metering request metrics from the HTTP method and path, so the internal
// runtime API is grouped into a small set of named operations.
func meteringOperation(method, path string) string {
	requestPath, _, _ := strings.Cut(path, "?")
	base := strings.TrimPrefix(requestPath, "/internal/")

	switch {
	case method == http.MethodPost && strings.HasSuffix(requestPath, "/reservations"):
		return "reserve"
	case method == http.MethodPost && strings.Contains(requestPath, "/reservations/") && strings.HasSuffix(requestPath, "/complete"):
		return "complete"
	case method == http.MethodPost && strings.HasSuffix(requestPath, "/usage-failures"):
		return "record_failure"
	case method == http.MethodPost && strings.HasSuffix(requestPath, "/authenticate"):
		return "authenticate"
	case method == http.MethodGet && strings.HasSuffix(requestPath, "/models"):
		return "list_models"
	case method == http.MethodGet && strings.Contains(requestPath, "/models/"):
		return "get_model"
	case method == http.MethodGet && strings.HasSuffix(requestPath, "/routers"):
		return "list_routers"
	case method == http.MethodGet && strings.Contains(requestPath, "/routers/"):
		return "get_router"
	case method == http.MethodPost && strings.HasSuffix(requestPath, "/tinfoil-proofs"):
		return "upsert_proof"
	case method == http.MethodGet && strings.Contains(requestPath, "/tinfoil-proofs"):
		return "list_proofs"
	default:
		return strings.Trim(base, "/")
	}
}

func reservationIDValue(reservationID *string) string {
	if reservationID == nil {
		return ""
	}
	return *reservationID
}

type reserveRequest struct {
	GatewayRequestID       string                        `json:"gateway_request_id"`
	AccountID              string                        `json:"account_id"`
	APIKeyID               string                        `json:"api_key_id"`
	Endpoint               string                        `json:"endpoint"`
	PublicModelID          string                        `json:"public_model_id"`
	RequestedPublicModelID string                        `json:"requested_public_model_id"`
	RouterID               *string                       `json:"router_id,omitempty"`
	RoutedPublicModelID    *string                       `json:"routed_public_model_id,omitempty"`
	MatchedCategory        *string                       `json:"matched_category,omitempty"`
	RoutingScore           *float32                      `json:"routing_score,omitempty"`
	RoutingCategoryScores  []domain.RoutingCategoryScore `json:"routing_category_scores,omitempty"`
	DecisionReason         *string                       `json:"decision_reason,omitempty"`
	FallbackUsed           *bool                         `json:"fallback_used,omitempty"`
	Stream                 bool                          `json:"stream"`
	MaxOutputTokens        *int                          `json:"max_output_tokens,omitempty"`
}

type reserveResponse struct {
	ReservationID string `json:"reservation_id"`
}

type completeRequest struct {
	ProviderName      string        `json:"provider_name,omitempty"`
	ProviderModelID   string        `json:"provider_model_id,omitempty"`
	ProviderRequestID string        `json:"provider_request_id,omitempty"`
	FinishReason      *string       `json:"finish_reason,omitempty"`
	Usage             *domain.Usage `json:"usage,omitempty"`
	LatencyMs         int64         `json:"latency_ms"`
}

type completeResponse struct {
	UsageEventID string `json:"usage_event_id,omitempty"`
}

type failureRequest struct {
	ReservationID          string                        `json:"reservation_id,omitempty"`
	GatewayRequestID       string                        `json:"gateway_request_id,omitempty"`
	AccountID              string                        `json:"account_id,omitempty"`
	APIKeyID               string                        `json:"api_key_id,omitempty"`
	Endpoint               string                        `json:"endpoint,omitempty"`
	PublicModelID          string                        `json:"public_model_id,omitempty"`
	RequestedPublicModelID string                        `json:"requested_public_model_id,omitempty"`
	RouterID               *string                       `json:"router_id,omitempty"`
	RoutedPublicModelID    *string                       `json:"routed_public_model_id,omitempty"`
	MatchedCategory        *string                       `json:"matched_category,omitempty"`
	RoutingScore           *float32                      `json:"routing_score,omitempty"`
	RoutingCategoryScores  []domain.RoutingCategoryScore `json:"routing_category_scores,omitempty"`
	DecisionReason         *string                       `json:"decision_reason,omitempty"`
	FallbackUsed           *bool                         `json:"fallback_used,omitempty"`
	ProviderName           string                        `json:"provider_name,omitempty"`
	ProviderModelID        string                        `json:"provider_model_id,omitempty"`
	Stream                 bool                          `json:"stream"`
	ErrorType              string                        `json:"error_type,omitempty"`
	ErrorCode              string                        `json:"error_code,omitempty"`
	PartialUsage           *domain.Usage                 `json:"partial_usage,omitempty"`
	LatencyMs              int64                         `json:"latency_ms"`
}

type errorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
