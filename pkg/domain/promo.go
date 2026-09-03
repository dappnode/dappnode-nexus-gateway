package domain

import (
	"regexp"
	"strings"
	"time"
)

// Promo code configuration constants.
const (
	PromoCodeMinLength = 4
	PromoCodeMaxLength = 64

	// NewUserGraceWindow is how long after account creation a "new users only"
	// promo can still be redeemed. The auto-redeem path fires immediately after
	// Authgear signup (account is auto-provisioned moments before), but a slack
	// window keeps the experience resilient to clock skew, slow UI loads, and
	// manual "redeem on Billing" attempts by a brand-new user.
	NewUserGraceWindow = 24 * time.Hour
)

// promoCodePattern matches normalized promo codes: uppercase letters, digits,
// underscores and hyphens.
var promoCodePattern = regexp.MustCompile(`^[A-Z0-9_-]+$`)

// PromoCode represents an operator-issued code that grants prepaid credit.
type PromoCode struct {
	ID             string
	Code           string
	AmountCents    int64
	Currency       string
	MaxRedemptions *int
	NewUsersOnly   bool
	Active         bool
	ExpiresAt      *time.Time
	Description    *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// RedemptionCount is the number of redemptions recorded for this code.
	// Populated only by list/get queries that aggregate redemptions; zero elsewhere.
	RedemptionCount int
}

// RemainingRedemptions reports how many more redemptions are allowed, or nil
// when MaxRedemptions is unset (unlimited).
func (p PromoCode) RemainingRedemptions() *int {
	if p.MaxRedemptions == nil {
		return nil
	}
	remaining := *p.MaxRedemptions - p.RedemptionCount
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

// IsExpiredAt reports whether the promo code is past its expiry at the given time.
func (p PromoCode) IsExpiredAt(now time.Time) bool {
	return p.ExpiresAt != nil && !now.Before(*p.ExpiresAt)
}

// PromoRedemption records a single successful redemption of a promo code.
type PromoRedemption struct {
	ID               string
	PromoCodeID      string
	AccountID        string
	AccountName      *string
	AmountMicrocents int64
	CreatedAt        time.Time
}

// RedeemResult is returned by the redemption flow. Applied is false when the
// account had already redeemed this code (idempotent success) — in that case
// Redemption holds the original redemption row.
type RedeemResult struct {
	Applied    bool
	Redemption PromoRedemption
	PromoCode  PromoCode
}

// NormalizePromoCode uppercases and trims the input and validates its shape.
// Returns the normalized code and whether it is valid.
func NormalizePromoCode(raw string) (string, bool) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if len(code) < PromoCodeMinLength || len(code) > PromoCodeMaxLength {
		return "", false
	}
	if !promoCodePattern.MatchString(code) {
		return "", false
	}
	return code, true
}

// IsNewUserAt reports whether accountCreatedAt falls inside the new-user grace
// window ending at now. Used to enforce PromoCode.NewUsersOnly.
func IsNewUserAt(accountCreatedAt, now time.Time) bool {
	return now.Before(accountCreatedAt.Add(NewUserGraceWindow))
}

// Promo error constructors. These map to the control-plane error codes.
// 410 Gone signals "this code no longer accepts redemptions" (deactivated or
// expired); 409 Conflict signals "you already redeemed this code" or "you are
// not eligible"; 422 signals the cap was hit.

func ErrPromoNotFound(code string) *GatewayError {
	return &GatewayError{HTTPStatus: 404, Type: ErrTypeInvalidRequest, Code: ErrCodeNotFound, Message: "promo code '" + code + "' not found"}
}

func ErrPromoInactive(code string) *GatewayError {
	return &GatewayError{HTTPStatus: 410, Type: ErrTypeInvalidRequest, Code: ErrCodeConflict, Message: "promo code '" + code + "' is no longer active"}
}

func ErrPromoExpired(code string) *GatewayError {
	return &GatewayError{HTTPStatus: 410, Type: ErrTypeInvalidRequest, Code: ErrCodeConflict, Message: "promo code '" + code + "' has expired"}
}

func ErrPromoMaxRedemptionsReached(code string) *GatewayError {
	return &GatewayError{HTTPStatus: 422, Type: ErrTypeInvalidRequest, Code: ErrCodeConflict, Message: "promo code '" + code + "' has reached its maximum redemptions"}
}

func ErrPromoNotEligibleNewUsersOnly(code string) *GatewayError {
	return &GatewayError{HTTPStatus: 409, Type: ErrTypeInvalidRequest, Code: ErrCodeConflict, Message: "promo code '" + code + "' is only available to new users"}
}

func ErrPromoAlreadyRedeemed(code string) *GatewayError {
	return &GatewayError{HTTPStatus: 409, Type: ErrTypeInvalidRequest, Code: ErrCodeConflict, Message: "promo code '" + code + "' has already been redeemed"}
}
