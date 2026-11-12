package mapper

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/jpeg"
	_ "image/png"
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
	if err := validateImageInputs(fields); err != nil {
		return domain.GenerateRequest{}, err
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

// HasImageInputs distinguishes editing from text-to-image requests.
func HasImageInputs(raw []byte) bool {
	var body map[string]json.RawMessage
	_ = json.Unmarshal(raw, &body)
	_, ok := body["images"]
	return ok
}

func validateImageInputs(fields map[string]json.RawMessage) error {
	raw, exists := fields["images"]
	if !exists {
		return nil
	}
	var images []struct {
		ImageURL string `json:"image_url"`
	}
	if json.Unmarshal(raw, &images) != nil || len(images) < 1 || len(images) > 4 {
		return domain.ErrInvalidField("images must contain 1 to 4 inline reference images")
	}
	total := 0
	for _, item := range images {
		header, encoded, ok := strings.Cut(item.ImageURL, ",")
		if !ok || (header != "data:image/png;base64" && header != "data:image/jpeg;base64" && header != "data:image/webp;base64") {
			return domain.ErrInvalidField("reference images must be PNG, JPEG or WebP base64 data URLs; remote URLs are not accepted")
		}
		if len(encoded) > 14<<20 {
			return domain.ErrInvalidField("each reference image must be at most 10 MiB")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) > 10<<20 {
			return domain.ErrInvalidField("invalid reference image or image exceeds 10 MiB")
		}
		total += len(decoded)
		cfg, format, err := image.DecodeConfig(bytes.NewReader(decoded))
		if err != nil || "data:image/"+format+";base64" != header || cfg.Width < 128 || cfg.Height < 128 || cfg.Width > 8192 || cfg.Height > 8192 || cfg.Width > 4*cfg.Height || cfg.Height > 4*cfg.Width {
			return domain.ErrInvalidField("reference images must be 128–8192 px on each side, with an aspect ratio at most 4:1")
		}
	}
	if total > 20<<20 {
		return domain.ErrInvalidField("reference images must total at most 20 MiB")
	}
	return nil
}
