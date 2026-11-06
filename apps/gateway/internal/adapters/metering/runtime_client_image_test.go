package metering

import "testing"

// An image-only model has no context window or output limit; one such model
// must not make the whole catalog invalid.
func TestValidateRuntimePublicModelAcceptsImageOnlyModel(t *testing.T) {
	no, yes := false, true
	zero, price := int64(0), int64(2_000_000)
	model := runtimePublicModel{
		ID: "id", PublicModelID: "flux", DisplayName: "FLUX", ProviderModelID: "pm", UpstreamModelName: "flux",
		ProviderConfig:                runtimeProviderConfig{ID: "pc", ProviderName: "phala", BaseURL: "https://x", APIKeySecretRef: "PHALA_API_KEY", Active: true},
		Active:                        true,
		SupportsChatCompletions:       &no,
		SupportsChatCompletionsStream: &no, SupportsTools: &no, SupportsParallelToolCalls: &no,
		SupportsStructuredOutput: &no, SupportsReasoning: &no,
		SupportsImageGenerations: true, PricePerImageMicrocents: &price,
		ProofMode: "none", Currency: "EUR",
		InputPricePer1MTokensMicrocents: &zero, OutputPricePer1MTokensMicrocents: &zero,
	}
	if err := validateRuntimePublicModel(model, ""); err != nil {
		t.Fatalf("image-only model rejected: %v", err)
	}
	model.SupportsChatCompletions = &yes
	if err := validateRuntimePublicModel(model, ""); err == nil {
		t.Fatal("a chat model still needs a context window")
	}
	negative := int64(-1)
	model.SupportsChatCompletions, model.PricePerImageMicrocents = &no, &negative
	if err := validateRuntimePublicModel(model, ""); err == nil {
		t.Fatal("a negative image price must be rejected")
	}
}
