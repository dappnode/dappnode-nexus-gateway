package main

import "testing"

func envFrom(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestGuestPolicyFromEnv_Defaults(t *testing.T) {
	policy, err := guestPolicyFromEnv(envFrom(nil))
	if err != nil {
		t.Fatalf("guestPolicyFromEnv: %v", err)
	}
	if len(policy.Models) != 1 || !policy.Models[defaultGuestModels] {
		t.Fatalf("models = %v", policy.Models)
	}
	if policy.MaxRequestBytes != 16384 || policy.MaxOutputTokens != 1024 {
		t.Fatalf("limits = %d bytes, %d tokens", policy.MaxRequestBytes, policy.MaxOutputTokens)
	}
}

func TestGuestPolicyFromEnv_ReadsOverrides(t *testing.T) {
	policy, err := guestPolicyFromEnv(envFrom(map[string]string{
		"GUEST_MODELS":            " a/model , b/model ,",
		"GUEST_MAX_REQUEST_BYTES": "8192",
		"GUEST_MAX_OUTPUT_TOKENS": "512",
	}))
	if err != nil {
		t.Fatalf("guestPolicyFromEnv: %v", err)
	}
	if len(policy.Models) != 2 || !policy.Models["a/model"] || !policy.Models["b/model"] {
		t.Fatalf("models = %v", policy.Models)
	}
	if policy.MaxRequestBytes != 8192 || policy.MaxOutputTokens != 512 {
		t.Fatalf("limits = %d bytes, %d tokens", policy.MaxRequestBytes, policy.MaxOutputTokens)
	}
}

// A typo must stop the gateway, not silently loosen or drop a guest limit.
func TestGuestPolicyFromEnv_RejectsInvalidLimits(t *testing.T) {
	for _, env := range []map[string]string{
		{"GUEST_MAX_REQUEST_BYTES": "16k"},
		{"GUEST_MAX_REQUEST_BYTES": "0"},
		{"GUEST_MAX_OUTPUT_TOKENS": "-1"},
		{"GUEST_MAX_OUTPUT_TOKENS": "1e3"},
	} {
		if _, err := guestPolicyFromEnv(envFrom(env)); err == nil {
			t.Errorf("accepted %v", env)
		}
	}
}
