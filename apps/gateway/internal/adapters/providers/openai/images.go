package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// maxImageResponseSize bounds an image generation response: a few base64
// images. Phala caps its own responses at 64 MiB.
const maxImageResponseSize = 64 << 20

// PrepareImageBody turns the client's image request into the provider's.
// Like chat, every field the client sent goes upstream unchanged, except the
// model (the provider's name for it) and gateway-only fields.
func PrepareImageBody(raw []byte, model domain.PublicModel) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // Numbers are forwarded exactly as sent.
	var body map[string]any
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, domain.ErrInvalidField("invalid JSON body")
	}
	body["model"] = model.UpstreamModelName
	for _, field := range gatewayOnlyFields {
		delete(body, field)
	}
	return body, nil
}

// GenerateImages sends an OpenAI images/generations request and returns the
// provider's JSON. The provider must return the images inline (b64_json).
func (a *Adapter) GenerateImages(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, error) {
	if strings.EqualFold(model.ProviderConfig.ProviderName, "minimax") {
		return a.generateMiniMaxImages(ctx, raw, model)
	}
	body, err := PrepareImageBody(raw, model)
	if err != nil {
		return nil, err
	}
	var out []byte
	_, err = a.proxy(ctx, body, nil, model, func(ctx context.Context, apiKey string, body map[string]any) (*http.Response, error) {
		var err error
		out, err = a.client.DoEndpointLimit(ctx, model.ProviderConfig.BaseURL, "/images/generations", apiKey, body, maxImageResponseSize)
		if err != nil {
			return nil, err
		}
		return &http.Response{Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})
	return out, err
}
