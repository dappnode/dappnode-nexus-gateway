package openai

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// maxImageResponseSize bounds an image generation response: a few base64
// images, capped at 64 MiB.
const maxImageResponseSize = 64 << 20

// PrepareImageBody turns the client's image request into the provider's.
// It selects the provider's model and removes gateway-only fields. The H3
// adapter then enforces the supported output options.
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

// GenerateImages uses the supported MiniMax H3 generation/edit protocol.
func (a *Adapter) GenerateImages(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, error) {
	if !model.SupportsImageEdits() {
		return nil, domain.ErrUnsupportedFeature("unsupported image model: use MiniMax H3")
	}
	return a.generateMiniMaxH3Images(ctx, raw, model)
}
