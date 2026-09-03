package anthropic

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	enclavenetwork "github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/network"
)

// Client handles HTTP communication with the Anthropic API.
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

// jsonUnmarshalBytes is used by mapper.go
func jsonUnmarshalBytes(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func (c *Client) Do(ctx context.Context, baseURL, apiKey string, body map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.responseTimeout)
	defer cancel()

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := baseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

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

	endpoint := baseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

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

// ProviderHTTPError carries the upstream HTTP status code.
type ProviderHTTPError struct {
	StatusCode int
	Body       string
}

func (e *ProviderHTTPError) Error() string {
	return fmt.Sprintf("provider error (%d): %s", e.StatusCode, e.Body)
}

func parseProviderError(statusCode int, body []byte) error {
	var errResp struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := string(body)
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		msg = errResp.Error.Message
	}

	return &ProviderHTTPError{StatusCode: statusCode, Body: msg}
}
