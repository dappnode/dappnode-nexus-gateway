package openai

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	enclavenetwork "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/network"
)

// Client handles HTTP communication with OpenAI-compatible APIs.
type Client struct {
	httpClient      *http.Client
	responseTimeout time.Duration
}

func NewClient(timeout time.Duration) *Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	enclavenetwork.ConfigureProviderTransport(transport)
	return &Client{
		httpClient:      &http.Client{Transport: transport},
		responseTimeout: timeout,
	}
}

func (c *Client) Do(ctx context.Context, baseURL, apiKey string, body map[string]any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.responseTimeout)
	defer cancel()

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read provider response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, parseProviderError(resp.StatusCode, respBody)
	}

	return respBody, nil
}

func (c *Client) DoStream(ctx context.Context, baseURL, apiKey string, body map[string]any) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider stream request failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
		resp.Body.Close()
		return nil, parseProviderError(resp.StatusCode, body)
	}

	return resp, nil
}

// ProviderHTTPError carries the upstream HTTP status code so the gateway can
// propagate a meaningful status instead of always returning 502.
type ProviderHTTPError struct {
	StatusCode int
	Body       string
	RawBody    string
	Type       string
	Code       string
	Reason     string
	TraceID    string
}

func (e *ProviderHTTPError) Error() string {
	return fmt.Sprintf("provider error (%d): %s", e.StatusCode, e.Body)
}

func parseProviderError(statusCode int, body []byte) error {
	// Try OpenAI standard format: {"error":{"message":"..."}}
	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	msg := string(body)
	errType := ""
	errCode := ""
	reason := ""
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		msg = errResp.Error.Message
		errType = errResp.Error.Type
		errCode = stringifyProviderCode(errResp.Error.Code)
	} else {
		// Try Novita/flat format: {"message":"...","type":"..."}
		var flatErr struct {
			Message  string         `json:"message"`
			Type     string         `json:"type"`
			Code     any            `json:"code"`
			Reason   string         `json:"reason"`
			Metadata map[string]any `json:"metadata"`
		}
		if json.Unmarshal(body, &flatErr) == nil && flatErr.Message != "" {
			msg = flatErr.Message
			errType = flatErr.Type
			errCode = stringifyProviderCode(flatErr.Code)
			reason = flatErr.Reason
			if traceID, ok := flatErr.Metadata["trace_id"].(string); ok {
				return &ProviderHTTPError{
					StatusCode: statusCode,
					Body:       msg,
					RawBody:    string(body),
					Type:       errType,
					Code:       errCode,
					Reason:     reason,
					TraceID:    traceID,
				}
			}
		}
	}

	return &ProviderHTTPError{
		StatusCode: statusCode,
		Body:       msg,
		RawBody:    string(body),
		Type:       errType,
		Code:       errCode,
		Reason:     reason,
		TraceID:    extractTraceID(msg),
	}
}

func ParseProviderError(statusCode int, body []byte) error {
	return parseProviderError(statusCode, body)
}

func stringifyProviderCode(v any) string {
	if v == nil {
		return ""
	}
	switch typed := v.(type) {
	case string:
		return typed
	case float64:
		return fmt.Sprintf("%.0f", typed)
	default:
		return fmt.Sprint(typed)
	}
}

var traceIDPattern = regexp.MustCompile(`(?i)trace[_ -]?id[:= ]+([a-z0-9_-]+)`)

func extractTraceID(msg string) string {
	match := traceIDPattern.FindStringSubmatch(msg)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}
