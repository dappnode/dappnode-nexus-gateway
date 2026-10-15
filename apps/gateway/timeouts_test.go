package main

import (
	"testing"
	"time"
)

func TestTimeoutConfigurationRejectsInvalidOrOverflowingValues(t *testing.T) {
	for _, value := range []string{"0", "-1", "not-a-duration", "9223372036854775807"} {
		t.Setenv("TEST_STALL_TIMEOUT", value)
		if _, err := timeoutSeconds("TEST_STALL_TIMEOUT", time.Minute); err == nil {
			t.Fatalf("accepted invalid duration %q", value)
		}
	}
	t.Setenv("TEST_STALL_TIMEOUT", "900")
	if got, err := timeoutSeconds("TEST_STALL_TIMEOUT", time.Minute); err != nil || got != 15*time.Minute {
		t.Fatalf("configured timeout = %v, %v", got, err)
	}
}
