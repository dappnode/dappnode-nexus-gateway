package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// H3 has an OpenAI-shaped JSON API, including inline image references.
func (a *Adapter) generateMiniMaxH3Images(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, error) {
	body, err := PrepareImageBody(raw, model)
	if err != nil {
		return nil, err
	}
	endpoint := "/images/generations"
	if _, ok := body["images"]; ok {
		endpoint = "/images/edits"
	}
	// A bounded 1K output keeps the initial UI price predictable. Other output
	// sizes can be offered as separately priced options in a later version.
	body["size"] = "auto"
	body["area"] = "1K"
	delete(body, "aspect_ratio")
	body["output_format"] = "png"
	body["response_format"] = "b64_json"
	body["stream"] = false
	var out []byte
	_, err = a.proxy(ctx, body, nil, model, func(ctx context.Context, apiKey string, body map[string]any) (*http.Response, error) {
		// H3 may wait 600 seconds before sending response headers. This dedicated
		// transport does not impose chat's shorter header/idle deadline.
		out, err = a.imageClient.DoEndpointLimit(ctx, model.ProviderConfig.BaseURL, endpoint, apiKey, body, maxImageResponseSize)
		if err != nil {
			return nil, err
		}
		return &http.Response{Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})
	if err != nil {
		return nil, err
	}
	// Partial failure slots have an error rather than image bytes. Only retain
	// successful images so billing and the UI count the actual outputs.
	var response map[string]json.RawMessage
	if err = json.Unmarshal(out, &response); err != nil {
		return nil, err
	}
	var data []map[string]json.RawMessage
	if err = json.Unmarshal(response["data"], &data); err != nil {
		return nil, err
	}
	successful := make([]map[string]json.RawMessage, 0, len(data))
	for _, item := range data {
		var b64 string
		_ = json.Unmarshal(item["b64_json"], &b64)
		if b64 != "" {
			delete(item, "url")
			successful = append(successful, item)
		}
	}
	response["data"], _ = json.Marshal(successful)
	return json.Marshal(response)
}
