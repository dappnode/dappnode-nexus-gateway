package registry

import (
	"fmt"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
)

// Registry selects provider adapters by name.
type Registry struct {
	providers       map[string]ports.Provider
	defaultProvider ports.Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]ports.Provider)}
}

func (r *Registry) Register(name string, provider ports.Provider) {
	r.providers[name] = provider
}

// SetDefault sets a fallback adapter returned when no provider is explicitly
// registered under the requested name.
func (r *Registry) SetDefault(provider ports.Provider) {
	r.defaultProvider = provider
}

func (r *Registry) GetProvider(providerName string) (ports.Provider, error) {
	if p, ok := r.providers[providerName]; ok {
		return p, nil
	}
	if r.defaultProvider != nil {
		return r.defaultProvider, nil
	}
	return nil, fmt.Errorf("unknown provider: %s", providerName)
}
