package domain

import "time"

// UsageEvent represents a single usage event as stored in the database.
type UsageEvent struct {
	ID                                   string
	RequestID                            *string
	AccountID                            string
	APIKeyID                             string
	PublicModelID                        *string
	ProviderModelID                      *string
	RouterID                             *string
	RoutedPublicModelID                  *string
	PublicModelName                      *string
	ProviderName                         *string
	ProviderRequestID                    *string
	InputTokens                          *int64
	OutputTokens                         *int64
	CacheCreationTokens                  *int64
	CacheReadTokens                      *int64
	InputPricePer1MTokensMicrocents      *int64
	OutputPricePer1MTokensMicrocents     *int64
	CacheReadPricePer1MTokensMicrocents  *int64
	CacheWritePricePer1MTokensMicrocents *int64
	CostMicrocents                       *int64
	ChargedMonthlyMicrocents             *int64
	ChargedPrepaidMicrocents             *int64
	LatencyMs                            *int64
	CreatedAt                            time.Time
	// AccountEmail is populated by list queries that JOIN accounts; nil elsewhere.
	AccountEmail *string
}

// UsageSummary holds aggregated usage data for an account or globally.
type UsageSummary struct {
	TotalRequests       int64
	TotalInputTokens    int64
	TotalOutputTokens   int64
	TotalCostMicrocents int64
	AvgLatencyMs        float64
	From                time.Time
	To                  time.Time
}

// UsageTimeBucket holds usage data aggregated for a single time bucket.
type UsageTimeBucket struct {
	Bucket              time.Time
	TotalRequests       int64
	TotalInputTokens    int64
	TotalOutputTokens   int64
	TotalCostMicrocents int64
}

// UsageEventRoutingDetails is the minimal projection used to surface the
// router decision behind a single usage event. RoutingScore is snapshotted
// at decision time. Threshold is the current configured threshold of the
// matched category (looked up at read time, not snapshotted), and is nil
// when no category matched or the category was since deleted.
type UsageEventRoutingDetails struct {
	EventID          string
	RouterID         *string
	MatchedCategory  *string
	RoutingScore     *float32
	CategoryScores   []RoutingCategoryScore
	MatchedThreshold *float32
	DecisionReason   *string
	FallbackUsed     *bool
}

// UsageByDimension holds usage data aggregated by an arbitrary grouping key
// (e.g. model name, provider name, or account ID).
type UsageByDimension struct {
	Key                 string
	Label               string
	TotalRequests       int64
	TotalInputTokens    int64
	TotalOutputTokens   int64
	TotalCostMicrocents int64
}
