package domain

import (
	"testing"
	"time"
)

func TestNormalizePromoCode(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantOk  bool
	}{
		{"WELCOME5", "WELCOME5", true},
		{"  welcome5  ", "WELCOME5", true},
		{"Welcome-5", "WELCOME-5", true},
		{"BONUS_CODE-3", "BONUS_CODE-3", true},
		{"ABC", "", false},          // too short
		{"", "", false},             // empty
		{"WITH SPACE", "", false},   // space not allowed
		{"BAD!CODE", "", false},     // invalid char
		{"LOWER1234", "LOWER1234", true},
		{"a_b-c1234", "A_B-C1234", true},
	}
	for _, c := range cases {
		got, ok := NormalizePromoCode(c.in)
		if got != c.want || ok != c.wantOk {
			t.Errorf("NormalizePromoCode(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantOk)
		}
	}
}

func TestNormalizePromoCode_TooLong(t *testing.T) {
	long := ""
	for i := 0; i < PromoCodeMaxLength+1; i++ {
		long += "A"
	}
	if _, ok := NormalizePromoCode(long); ok {
		t.Errorf("NormalizePromoCode accepted a code longer than %d chars", PromoCodeMaxLength)
	}
}

func TestPromoCodeRemainingRedemptions(t *testing.T) {
	// unlimited
	unlimited := PromoCode{MaxRedemptions: nil, RedemptionCount: 5}
	if r := unlimited.RemainingRedemptions(); r != nil {
		t.Errorf("unlimited code returned remaining %v, want nil", *r)
	}

	// 100 / 5 used → 95
	max := 100
	p := PromoCode{MaxRedemptions: &max, RedemptionCount: 5}
	r := p.RemainingRedemptions()
	if r == nil || *r != 95 {
		t.Errorf("remaining = %v, want 95", r)
	}

	// exhausted clamps to 0
	exhausted := PromoCode{MaxRedemptions: &max, RedemptionCount: 150}
	r = exhausted.RemainingRedemptions()
	if r == nil || *r != 0 {
		t.Errorf("remaining = %v, want 0", r)
	}
}

func TestPromoCodeIsExpiredAt(t *testing.T) {
	expiry := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := PromoCode{ExpiresAt: &expiry}
	if p.IsExpiredAt(expiry) != true {
		t.Error("code should be expired at expiry time")
	}
	if p.IsExpiredAt(expiry.Add(-time.Second)) != false {
		t.Error("code should not be expired one second before expiry")
	}
	noExpiry := PromoCode{}
	if noExpiry.IsExpiredAt(time.Now()) != false {
		t.Error("code with nil expiry should never be expired")
	}
}

func TestIsNewUserAt(t *testing.T) {
	created := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	// Within window
	if !IsNewUserAt(created, created.Add(time.Hour)) {
		t.Error("1h after creation should be new user")
	}
	// Exactly at window boundary → no longer new (exclusive)
	if IsNewUserAt(created, created.Add(NewUserGraceWindow)) {
		t.Error("at grace boundary should not be new user")
	}
	// Well after window
	if IsNewUserAt(created, created.Add(48*time.Hour)) {
		t.Error("48h after creation should not be new user")
	}
}
