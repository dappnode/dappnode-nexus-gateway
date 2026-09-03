package domain

// ProviderCatalogEntry is one model offered by a provider, as discovered from
// the provider's own model-listing API. Prices are the provider's raw rate in
// USD per 1M tokens, before any FX conversion or markup.
type ProviderCatalogEntry struct {
	ProviderModelName string
	Title             string
	Description       string
	ContextSize       int64
	MaxOutputTokens   int64
	InputUSDPer1M     float64
	OutputUSDPer1M    float64
	SupportsTools     bool
	SupportsReasoning bool
}
