package ports

// ProviderRegistry selects a provider adapter by name.
type ProviderRegistry interface {
	GetProvider(providerName string) (GenerationProvider, error)
}
