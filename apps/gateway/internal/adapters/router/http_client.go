package router

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	enclavenetwork "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/network"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Client calls the standalone router service over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	enclavenetwork.ConfigureRouterTransport(transport)
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
}

type routeRequest struct {
	RouterID string                 `json:"router_id"`
	Request  domain.GenerateRequest `json:"request"`
}

type routeResponse struct {
	PublicModelID  string                        `json:"public_model_id"`
	Category       *string                       `json:"category,omitempty"`
	Score          *float32                      `json:"score,omitempty"`
	CategoryScores []domain.RoutingCategoryScore `json:"category_scores,omitempty"`
	FallbackUsed   bool                          `json:"fallback_used"`
	Reason         string                        `json:"reason"`
}

type errorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) Route(ctx context.Context, req domain.RouteRequest) (domain.RouteDecision, error) {
	body, err := json.Marshal(routeRequest{
		RouterID: req.RouterID,
		Request:  req.Request,
	})
	if err != nil {
		return domain.RouteDecision{}, routerInternalError("reason", "failed to encode router request: "+err.Error())
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/route", bytes.NewReader(body))
	if err != nil {
		return domain.RouteDecision{}, routerInternalError("reason", "failed to build router request: "+err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		metrics.RouterRequests.WithLabelValues("transport_error").Inc()
		metrics.RouterDuration.Observe(time.Since(start).Seconds())
		return domain.RouteDecision{}, routerInternalError("reason", err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		metrics.RouterRequests.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()
		metrics.RouterDuration.Observe(time.Since(start).Seconds())
		var errResp errorResponse
		if decodeErr := json.NewDecoder(resp.Body).Decode(&errResp); decodeErr == nil && errResp.Error.Code != "" {
			return domain.RouteDecision{}, mapRouterError(req.RouterID, resp.StatusCode, errResp)
		}
		return domain.RouteDecision{}, routerInternalError("router_status", resp.StatusCode)
	}

	var out routeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		metrics.RouterRequests.WithLabelValues("protocol_error").Inc()
		metrics.RouterDuration.Observe(time.Since(start).Seconds())
		return domain.RouteDecision{}, routerInternalError("reason", err.Error())
	}

	metrics.RouterRequests.WithLabelValues("success").Inc()
	metrics.RouterDuration.Observe(time.Since(start).Seconds())
	return domain.RouteDecision{
		PublicModelID:  out.PublicModelID,
		Category:       out.Category,
		Score:          out.Score,
		CategoryScores: out.CategoryScores,
		FallbackUsed:   out.FallbackUsed,
		Reason:         out.Reason,
	}, nil
}

func mapRouterError(routerID string, status int, errResp errorResponse) error {
	switch {
	case status == http.StatusNotFound &&
		errResp.Error.Type == domain.ErrTypeInvalidRequest &&
		(errResp.Error.Code == domain.ErrCodeNotFound || errResp.Error.Code == domain.ErrCodeUnsupportedModel) &&
		errResp.Error.Message != "":
		return domain.ErrUnsupportedModel(routerID)
	case status == http.StatusBadRequest &&
		errResp.Error.Type == domain.ErrTypeInvalidRequest &&
		errResp.Error.Code == domain.ErrCodeInvalidField &&
		errResp.Error.Message != "":
		return domain.ErrInvalidField(errResp.Error.Message)
	default:
		return routerInternalError(
			"router_status", status,
			"router_code", errResp.Error.Code,
			"reason", errResp.Error.Message,
		)
	}
}

func routerInternalError(fields ...any) *domain.GatewayError {
	metadata := []any{"dependency", "router"}
	metadata = append(metadata, fields...)
	return domain.ErrInternal("an internal error occurred").WithMeta(metadata...)
}
