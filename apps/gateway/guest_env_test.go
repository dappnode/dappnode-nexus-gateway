package main

import "testing"

func TestPositiveIntEnv(t *testing.T) {
	t.Setenv("GUEST_MAX_OUTPUT_TOKENS", "")
	if n, err := positiveIntEnv("GUEST_MAX_OUTPUT_TOKENS", 8192); err != nil || n != 8192 {
		t.Fatalf("unset = %d, %v; want the default", n, err)
	}
	t.Setenv("GUEST_MAX_OUTPUT_TOKENS", "4096")
	if n, err := positiveIntEnv("GUEST_MAX_OUTPUT_TOKENS", 8192); err != nil || n != 4096 {
		t.Fatalf("4096 = %d, %v", n, err)
	}
	for _, bad := range []string{"0", "-1", "8k"} {
		t.Setenv("GUEST_MAX_OUTPUT_TOKENS", bad)
		if _, err := positiveIntEnv("GUEST_MAX_OUTPUT_TOKENS", 8192); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}
