package metering

import "testing"

// An image-only model has no context window or output limit; one such model
// must not make the whole catalog invalid.
func TestValidateRuntimePublicModelAcceptsImageOnlyModel(t *testing.T) {
	no, yes := false, true
	zero, price := int64(0), int64(2_000_000)
	model := runtimePublicModel{
		ID: "id", PublicModelID: "minimax/h3-image", DisplayName: "MiniMax H3 Image", ProviderModelID: "pm", UpstreamModelName: "MiniMax-H3-Image",
		ProviderConfig:                runtimeProviderConfig{ID: "pc", ProviderName: "minimax", BaseURL: "https://x", APIKeySecretRef: "MINIMAX_API_KEY", Active: true},
		Active:                        true,
		SupportsChatCompletions:       &no,
		SupportsChatCompletionsStream: &no, SupportsTools: &no, SupportsParallelToolCalls: &no,
		SupportsStructuredOutput: &no, SupportsReasoning: &no,
		SupportsImageGenerations: true, ImageInputPricePer1MTokensMicrocents: &price, ImageMaxCostPerImageMicrocents: &price,
		ProofMode: "none", Currency: "EUR",
		InputPricePer1MTokensMicrocents: &zero, OutputPricePer1MTokensMicrocents: &zero,
	}
	if err := validateRuntimePublicModel(model, ""); err != nil {
		t.Fatalf("image-only model rejected: %v", err)
	}
	for _, field := range []string{"reference rate", "ceiling"} {
		invalid := model
		if field == "reference rate" {
			invalid.ImageInputPricePer1MTokensMicrocents = nil
		} else {
			invalid.ImageMaxCostPerImageMicrocents = nil
		}
		if err := validateRuntimePublicModel(invalid, ""); err == nil {
			t.Fatalf("image model missing %s accepted", field)
		}
	}
	model.SupportsChatCompletions = &yes
	if err := validateRuntimePublicModel(model, ""); err == nil {
		t.Fatal("a chat model still needs a context window")
	}
	negative := int64(-1)
	model.SupportsChatCompletions, model.ImageInputPricePer1MTokensMicrocents = &no, &negative
	if err := validateRuntimePublicModel(model, ""); err == nil {
		t.Fatal("a negative image price must be rejected")
	}
}
