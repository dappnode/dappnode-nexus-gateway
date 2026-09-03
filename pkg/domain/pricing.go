package domain

import "time"

// ModelPricing represents pricing for a provider model at a point in time.
type ModelPricing struct {
	ID                                    string
	ProviderModelID                       string
	InputPricePer1MTokensMicrocents       int64
	OutputPricePer1MTokensMicrocents      int64
	CacheReadPricePer1MTokensMicrocents   *int64
	CacheWritePricePer1MTokensMicrocents  *int64
	EffectiveFrom                         time.Time
}
