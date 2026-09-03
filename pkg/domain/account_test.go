package domain_test

import (
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestNormalizeAPIKeyPIIMode(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		want   string
		wantOK bool
	}{
		{name: "empty defaults off", mode: "", want: domain.APIKeyPIIModeOff, wantOK: true},
		{name: "off", mode: domain.APIKeyPIIModeOff, want: domain.APIKeyPIIModeOff, wantOK: true},
		{name: "low", mode: domain.APIKeyPIIModeLow, want: domain.APIKeyPIIModeLow, wantOK: true},
		{name: "balanced", mode: domain.APIKeyPIIModeBalanced, want: domain.APIKeyPIIModeBalanced, wantOK: true},
		{name: "high", mode: domain.APIKeyPIIModeHigh, want: domain.APIKeyPIIModeHigh, wantOK: true},
		{name: "invalid", mode: "everything", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := domain.NormalizeAPIKeyPIIMode(tt.mode)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("NormalizeAPIKeyPIIMode(%q) = %q,%v want %q,%v", tt.mode, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
