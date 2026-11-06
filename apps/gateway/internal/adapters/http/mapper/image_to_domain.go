package mapper

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// MaxImagesPerRequest bounds n on /v1/images/generations, so one request's
// hold and response stay small.
const MaxImagesPerRequest = 4

// imageRequest is what the gateway reads from an images/generations body.
// The body itself is forwarded as sent.
type imageRequest struct {
	Model          string          `json:"model"`
	Prompt         *string         `json:"prompt"`
	N              json.RawMessage `json:"n"`
	Stream         *bool           `json:"stream"`
	ResponseFormat *string         `json:"response_format"`
}

// ImageGenerationRequestToDomain validates a /v1/images/generations body:
// a model, a prompt, and how many images (n, 1 by default). Images always
// come back inline as b64_json; streaming partial images is not offered.
func ImageGenerationRequestToDomain(raw json.RawMessage) (domain.GenerateRequest, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return domain.GenerateRequest{}, domain.ErrInvalidField("invalid JSON body")
	}
	var req imageRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return domain.GenerateRequest{}, domain.ErrInvalidField("invalid request body: " + err.Error())
	}
	if strings.TrimSpace(req.Model) == "" {
		return domain.GenerateRequest{}, domain.ErrInvalidField("model is required")
	}
	if req.Prompt == nil || strings.TrimSpace(*req.Prompt) == "" {
		return domain.GenerateRequest{}, domain.ErrInvalidField("prompt is required")
	}
	if req.Stream != nil && *req.Stream {
		return domain.GenerateRequest{}, domain.ErrUnsupportedFeature("streaming image generation")
	}
	if _, ok := fields["partial_images"]; ok {
		return domain.GenerateRequest{}, domain.ErrUnsupportedFeature("partial_images")
	}
	if req.ResponseFormat != nil && *req.ResponseFormat != "b64_json" {
		return domain.GenerateRequest{}, domain.ErrInvalidField("response_format must be b64_json: Nexus returns images inline and stores none")
	}
	n := 1
	if len(req.N) > 0 && string(req.N) != "null" {
		if err := json.Unmarshal(req.N, &n); err != nil || n < 1 || n > MaxImagesPerRequest {
			return domain.GenerateRequest{}, domain.ErrInvalidField(fmt.Sprintf("n must be a whole number from 1 to %d", MaxImagesPerRequest))
		}
	}
	return domain.GenerateRequest{
		PublicModelID:    req.Model,
		RequestedModelID: req.Model,
		Images:           &n,
	}, nil
}
