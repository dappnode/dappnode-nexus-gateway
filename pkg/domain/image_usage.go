package domain

import "encoding/json"

// ParseImageTokenUsage validates MiniMax's three separate billing dimensions.
// PromptTokens is text only; ImageInputTokens is never counted a second time.
func ParseImageTokenUsage(raw json.RawMessage, images int64) (Usage, bool) {
	var v struct {
		Input        *int64 `json:"input_tokens"`
		Output       *int64 `json:"output_tokens"`
		Total        *int64 `json:"total_tokens"`
		Images       *int64 `json:"generated_images"`
		InputDetails struct {
			Text  *int64 `json:"text_tokens"`
			Image *int64 `json:"image_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			Image *int64 `json:"image_tokens"`
		} `json:"output_tokens_details"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return Usage{}, false
	}
	for _, count := range []*int64{v.Input, v.Output, v.Total, v.Images, v.InputDetails.Text, v.InputDetails.Image, v.OutputDetails.Image} {
		if count == nil || *count < 0 || *count > 1_000_000_000 {
			return Usage{}, false
		}
	}
	if images <= 0 || *v.Images != images || *v.Input != *v.InputDetails.Text+*v.InputDetails.Image || *v.Output != *v.OutputDetails.Image || *v.Total != *v.Input+*v.Output || *v.Output == 0 {
		return Usage{}, false
	}
	return Usage{PromptTokens: *v.InputDetails.Text, ImageInputTokens: *v.InputDetails.Image, CompletionTokens: *v.OutputDetails.Image, TotalTokens: *v.Total, Images: images, ProviderUsage: raw}, true
}
