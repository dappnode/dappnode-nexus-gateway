package dto

// ModelsResponse is the response envelope for GET /v1/models.
type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelData `json:"data"`
}

// ModelData is a single model entry.
type ModelData struct {
	ID                              string   `json:"id"`
	Object                          string   `json:"object"`
	Kind                            string   `json:"kind"`
	BaseModel                       *string  `json:"base_model,omitempty"`
	OwnedBy                         string   `json:"owned_by"`
	Created                         int64    `json:"created"`
	DisplayName                     string   `json:"display_name"`
	Description                     *string  `json:"description,omitempty"`
	ContextSize                     *int     `json:"context_size,omitempty"`
	MaxOutputTokens                 *int     `json:"max_output_tokens,omitempty"`
	Currency                        *string  `json:"currency,omitempty"`
	InputPricePer1MTokensCents      *int64   `json:"input_price_per_1m_tokens_cents,omitempty"`
	OutputPricePer1MTokensCents     *int64   `json:"output_price_per_1m_tokens_cents,omitempty"`
	CacheReadPricePer1MTokensCents  *int64   `json:"cache_read_price_per_1m_tokens_cents,omitempty"`
	CacheWritePricePer1MTokensCents *int64   `json:"cache_write_price_per_1m_tokens_cents,omitempty"`
	InputPricePer1MTokensUSD        *float64 `json:"input_price_per_1m_tokens_usd,omitempty"`
	OutputPricePer1MTokensUSD       *float64 `json:"output_price_per_1m_tokens_usd,omitempty"`
	CacheReadPricePer1MTokensUSD    *float64 `json:"cache_read_price_per_1m_tokens_usd,omitempty"`
	CacheWritePricePer1MTokensUSD   *float64 `json:"cache_write_price_per_1m_tokens_usd,omitempty"`
	Features                        []string `json:"features"`
	Endpoints                       []string `json:"endpoints"`
	ProofMode                       string   `json:"proof_mode"`
	ProofsEnabled                   bool     `json:"proofs_enabled"`
}
