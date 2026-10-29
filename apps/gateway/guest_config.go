package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
)

// Guest chat defaults. At DeepSeek V4.1 Flash's prices the largest guest
// request (16 KiB in, 1,024 tokens out) reserves about 0.7 cents, inside the
// 1-cent credit the control plane grants each guest; shorter messages reserve
// proportionally less. Change these together with that credit; see the Nexus
// repo's docs/guest-chat.md.
const (
	defaultGuestModels          = "deepseek/deepseek-v4.1-flash"
	defaultGuestMaxRequestBytes = 16 << 10
	defaultGuestMaxOutputTokens = 1024
)

// guestPolicyFromEnv reads the guest chat limits. A set but invalid value is
// an error, so a typo cannot loosen them.
func guestPolicyFromEnv(getenv func(string) string) (services.GuestPolicy, error) {
	policy := services.GuestPolicy{Models: map[string]bool{}}
	models := defaultGuestModels
	if v, set := lookup(getenv, "GUEST_MODELS"); set {
		models = v
	}
	for _, model := range strings.Split(models, ",") {
		if model = strings.TrimSpace(model); model != "" {
			policy.Models[model] = true
		}
	}

	var err error
	if policy.MaxRequestBytes, err = positiveIntEnv(getenv, "GUEST_MAX_REQUEST_BYTES", defaultGuestMaxRequestBytes); err != nil {
		return services.GuestPolicy{}, err
	}
	if policy.MaxOutputTokens, err = positiveIntEnv(getenv, "GUEST_MAX_OUTPUT_TOKENS", defaultGuestMaxOutputTokens); err != nil {
		return services.GuestPolicy{}, err
	}
	return policy, nil
}

func lookup(getenv func(string) string, name string) (string, bool) {
	v := strings.TrimSpace(getenv(name))
	return v, v != ""
}

func positiveIntEnv(getenv func(string) string, name string, fallback int) (int, error) {
	v, set := lookup(getenv, name)
	if !set {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive whole number", name)
	}
	return n, nil
}
