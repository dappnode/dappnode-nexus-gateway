package domain

import "time"

// Account status constants.
const (
	AccountStatusActive   = "active"
	AccountStatusInactive = "inactive"
)

// Account plan constants.
const (
	AccountPlanFree         = "free"
	AccountPlanPro20Monthly = "pro_20_monthly"
)

// Account represents a gateway account.
type Account struct {
	ID                            string
	AuthgearSubjectID             *string
	ExternalCustomerID            *string
	Email                         *string
	Name                          *string
	Status                        string
	Plan                          string
	SubscriptionCancelAtPeriodEnd bool
	SubscriptionCancelAt          *time.Time
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
	// LastUsedAt is the most recent `last_used_at` across the account's API keys.
	// Populated only by list queries that aggregate key usage; nil elsewhere.
	LastUsedAt *time.Time
}

// IsActive returns true if the account status is "active".
func (a Account) IsActive() bool {
	return a.Status == AccountStatusActive
}

// SubscriptionStatusMessage returns a user-facing summary of subscription cancellation state.
func (a Account) SubscriptionStatusMessage() *string {
	if !a.SubscriptionCancelAtPeriodEnd {
		return nil
	}
	if a.SubscriptionCancelAt == nil {
		message := "scheduled to cancel at period end"
		return &message
	}

	message := "scheduled to cancel on " + a.SubscriptionCancelAt.UTC().Format("January 2, 2006")
	return &message
}

// APIKey represents a hashed API key record.
type APIKey struct {
	ID         string
	AccountID  string
	Name       *string
	KeyPrefix  string
	KeyHash    string
	Active     bool
	PIIMode    string
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// IsKeyActive returns true if the key is marked active.
func (k APIKey) IsKeyActive() bool {
	return k.Active
}

// API key PII masking levels. The level is stored per key and can be changed
// explicitly from the control plane.
const (
	APIKeyPIIModeOff      = "off"
	APIKeyPIIModeLow      = "low"
	APIKeyPIIModeBalanced = "balanced"
	APIKeyPIIModeHigh     = "high"
)

// DefaultAPIKeyPIIMode is used when callers omit pii_mode on key creation.
const DefaultAPIKeyPIIMode = APIKeyPIIModeOff

// NormalizeAPIKeyPIIMode returns a valid mode, defaulting empty input to off.
func NormalizeAPIKeyPIIMode(mode string) (string, bool) {
	if mode == "" {
		return DefaultAPIKeyPIIMode, true
	}
	switch mode {
	case APIKeyPIIModeOff, APIKeyPIIModeLow, APIKeyPIIModeBalanced, APIKeyPIIModeHigh:
		return mode, true
	default:
		return "", false
	}
}

// CreateAPIKeyParams holds parameters for creating a new API key.
type CreateAPIKeyParams struct {
	AccountID string
	Name      *string
	PIIMode   string
	ExpiresAt *time.Time
}

// CreateAPIKeyResult is returned after key creation, including the raw key shown only once.
type CreateAPIKeyResult struct {
	APIKey APIKey
	RawKey string
}

// AuthContext carries the authenticated account and API key for a request.
type AuthContext struct {
	Account Account
	APIKey  APIKey
}
