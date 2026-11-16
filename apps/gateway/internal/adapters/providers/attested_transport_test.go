package providers_test

import (
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/openai"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/tinfoil"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/typesafe"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
)

// Attested inference sends a prompt only to adapters that claim an attested
// transport, so the claim must be exactly where verification happens.
func TestOnlyTinfoilClaimsAnAttestedTransport(t *testing.T) {
	adapters := map[string]ports.Provider{
		"tinfoil":  tinfoil.NewAdapter(time.Second),
		"openai":   openai.NewAdapter(time.Second),
		"typesafe": typesafe.NewAdapter(time.Second),
	}
	for name, adapter := range adapters {
		attested, ok := adapter.(ports.AttestedProvider)
		got := ok && attested.AttestedTransport()
		if want := name == "tinfoil"; got != want {
			t.Errorf("%s adapter attested transport = %v, want %v", name, got, want)
		}
	}
}
