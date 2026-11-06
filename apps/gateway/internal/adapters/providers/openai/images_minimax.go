package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// MiniMax's image API is its own, not OpenAI's (checked 2026-11-06):
// POST {base}/image_generation with model, prompt, n, width/height or
// aspect_ratio and response_format=base64, answered synchronously with
// data.image_base64, metadata.failed_count (images its safety filter
// blocked) and base_resp.status_code (0 on success, also on HTTP 200).
// The gateway speaks OpenAI to the client and translates both ways.

// miniMaxImageBody builds MiniMax's request from an OpenAI one. Only fields
// MiniMax documents are sent: OpenAI's size becomes width and height; seed,
// aspect_ratio and prompt_optimizer pass through.
func miniMaxImageBody(raw []byte, model domain.PublicModel) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var req map[string]any
	if err := dec.Decode(&req); err != nil || req == nil {
		return nil, domain.ErrInvalidField("invalid JSON body")
	}
	body := map[string]any{
		"model":           model.UpstreamModelName,
		"prompt":          req["prompt"],
		"response_format": "base64",
	}
	if n, ok := req["n"]; ok && n != nil {
		body["n"] = n
	}
	for _, field := range []string{"seed", "aspect_ratio", "prompt_optimizer"} {
		if v, ok := req[field]; ok && v != nil {
			body[field] = v
		}
	}
	if size, _ := req["size"].(string); size != "" && size != "auto" {
		width, height, ok := parseImageSize(size)
		if !ok {
			return nil, domain.ErrInvalidField("size must be WIDTHxHEIGHT, each from 512 to 2048 and divisible by 8, or auto")
		}
		body["width"], body["height"] = width, height
	}
	return body, nil
}

func parseImageSize(size string) (int, int, bool) {
	w, h, ok := strings.Cut(strings.ToLower(size), "x")
	if !ok {
		return 0, 0, false
	}
	width, err1 := strconv.Atoi(w)
	height, err2 := strconv.Atoi(h)
	valid := func(v int) bool { return v >= 512 && v <= 2048 && v%8 == 0 }
	if err1 != nil || err2 != nil || !valid(width) || !valid(height) {
		return 0, 0, false
	}
	return width, height, true
}

type miniMaxImageResponse struct {
	ID   string `json:"id"`
	Data *struct {
		ImageBase64 []string `json:"image_base64"`
	} `json:"data"`
	Metadata *struct {
		SuccessCount json.Number `json:"success_count"`
		FailedCount  json.Number `json:"failed_count"`
	} `json:"metadata"`
	BaseResp *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

func (a *Adapter) generateMiniMaxImages(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, error) {
	body, err := miniMaxImageBody(raw, model)
	if err != nil {
		return nil, err
	}
	var out []byte
	_, err = a.proxy(ctx, body, []string{"openai_images=minimax_image_generation"}, model, func(ctx context.Context, apiKey string, body map[string]any) (*http.Response, error) {
		var err error
		out, err = a.client.DoEndpointLimit(ctx, model.ProviderConfig.BaseURL, "/image_generation", apiKey, body, maxImageResponseSize)
		if err != nil {
			return nil, err
		}
		return &http.Response{Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})
	if err != nil {
		return nil, err
	}
	return miniMaxToOpenAIImages(out)
}

// miniMaxToOpenAIImages turns MiniMax's answer into OpenAI's images response,
// or its error into the gateway's.
func miniMaxToOpenAIImages(body []byte) ([]byte, error) {
	var resp miniMaxImageResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, domain.ErrProviderError(http.StatusBadGateway, "provider returned an invalid response")
	}
	if resp.BaseResp != nil && resp.BaseResp.StatusCode != 0 {
		return nil, miniMaxImageError(resp.BaseResp.StatusCode, resp.BaseResp.StatusMsg)
	}
	var images []string
	if resp.Data != nil {
		images = resp.Data.ImageBase64
	}
	if len(images) == 0 {
		if resp.Metadata != nil {
			if failed, err := resp.Metadata.FailedCount.Int64(); err == nil && failed > 0 {
				// Every image was blocked by MiniMax's safety filter.
				return nil, domain.ErrContentPolicy().WithMeta("upstream_failed_count", failed)
			}
		}
		return nil, domain.ErrProviderError(http.StatusBadGateway, "provider returned no images")
	}
	data := make([]map[string]string, 0, len(images))
	for _, image := range images {
		data = append(data, map[string]string{"b64_json": image})
	}
	return json.Marshal(map[string]any{"created": time.Now().Unix(), "data": data})
}

// miniMaxImageError maps MiniMax's documented status codes.
func miniMaxImageError(code int, msg string) error {
	var gwErr *domain.GatewayError
	switch code {
	case 1026, 1027: // Sensitive input or output.
		gwErr = domain.ErrContentPolicy()
	case 1002, 1039: // Rate or token limit.
		gwErr = domain.ErrProviderError(http.StatusTooManyRequests, "provider rate limited: "+msg)
		gwErr.Code = domain.ErrCodeProviderRateLimited
	case 1004, 1008, 2049: // Authentication, Nexus's balance, invalid key.
		gwErr = domain.ErrProviderError(http.StatusBadGateway, "provider auth/permission error: "+msg)
		gwErr.Code = domain.ErrCodeProviderConfiguration
	case 2013: // Invalid parameters.
		gwErr = domain.ErrProviderError(http.StatusBadRequest, "provider rejected request: "+msg)
		gwErr.Code = domain.ErrCodeProviderRejectedRequest
	default:
		gwErr = domain.ErrProviderError(http.StatusBadGateway, fmt.Sprintf("provider returned an unsuccessful response (%d)", code))
	}
	return gwErr.WithMeta("upstream_code", strconv.Itoa(code), "upstream_error", msg)
}
