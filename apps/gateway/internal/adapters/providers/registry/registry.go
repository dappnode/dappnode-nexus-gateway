package registry

import (
	"fmt"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
)

// Registry selects provider adapters by name.
type Registry struct {
	providers       map[string]ports.GenerationProvider
	defaultProvider ports.GenerationProvider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]ports.GenerationProvider)}
}

func (r *Registry) Register(name string, provider ports.GenerationProvider) {
	r.providers[name] = provider
}

// SetDefault sets a fallback adapter returned when no provider is explicitly
// registered under the requested name.
func (r *Registry) SetDefault(provider ports.GenerationProvider) {
	r.defaultProvider = provider
}

func (r *Registry) GetProvider(providerName string) (ports.GenerationProvider, error) {
	if p, ok := r.providers[providerName]; ok {
		return p, nil
	}
	if r.defaultProvider != nil {
		return r.defaultProvider, nil
	}
	return nil, fmt.Errorf("unknown provider: %s", providerName)
}
