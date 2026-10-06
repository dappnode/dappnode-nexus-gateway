package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/openai"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type Adapter struct{ client *openai.Client }

func NewAdapter(timeout time.Duration) *Adapter { return &Adapter{client: openai.NewClient(timeout)} }

func (a *Adapter) Complete(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, *domain.TinfoilTransportProof, error) {
	apiKey := os.Getenv(model.ProviderConfig.APIKeySecretRef)
	if apiKey == "" {
		return nil, nil, domain.ErrInternal("provider API key is not configured")
	}
	var body map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, nil, domain.ErrInvalidField("invalid JSON body")
	}
	body["model"] = model.UpstreamModelName
	delete(body, "provider_options")
	out, err := a.client.DoEndpoint(ctx, model.ProviderConfig.BaseURL, "/systemone", apiKey, body)
	var upstream *openai.ProviderHTTPError
	if errors.As(err, &upstream) && (upstream.StatusCode == 422 || upstream.StatusCode == 429 || upstream.StatusCode == 529) {
		return nil, nil, domain.ErrProviderError(upstream.StatusCode, "TypeSafe rejected the request").WithMeta("upstream_status", upstream.StatusCode)
	}
	return out, nil, openai.MapProviderErrorWithCompatibilityContext(err, model, body)
}

func (a *Adapter) Stream(context.Context, []byte, domain.PublicModel) (ports.ProviderStream, error) {
	return ports.ProviderStream{}, domain.ErrUnsupportedFeature("streaming for systemone")
}
