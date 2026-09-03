package domain

import "github.com/shopspring/decimal"

const (
	ProofModeNone                     = "none"
	ProofModeTinfoilAttestedTransport = "tinfoil_attested_transport"
)

// ProviderConfig holds upstream provider connection details.
type ProviderConfig struct {
	ID              string
	ProviderName    string
	BaseURL         string
	APIKeySecretRef string
	OrganizationRef *string
	Active          bool
}

// ProviderTarget identifies one upstream execution target for a public model.
type ProviderTarget struct {
	ProviderModelID   string
	UpstreamModelName string
	ProviderConfig    ProviderConfig
}

// PublicModel is the fully-resolved model exposed by the gateway.
type PublicModel struct {
	ID                            string
	PublicModelID                 string
	DisplayName                   string
	Description                   *string
	ProviderModelID               string
	UpstreamModelName             string
	ProviderConfig                ProviderConfig
	Fallback                      *ProviderTarget
	SupportsChatCompletions       bool
	SupportsChatCompletionsStream bool
	SupportsTools                 bool
	SupportsParallelToolCalls     bool
	SupportsStructuredOutput      bool
	SupportsReasoning             bool
	ProofMode                     string
	MaxContextWindow              int
	MaxOutputTokens               int
	InputPricePerMillion          decimal.Decimal
	OutputPricePerMillion         decimal.Decimal
	CacheReadPricePerMillion      *decimal.Decimal
	CacheWritePricePerMillion     *decimal.Decimal
	Currency                      string
	Active                        bool
}

func (m PublicModel) EffectiveProofMode() string {
	if m.ProofMode != "" && m.ProofMode != ProofModeNone {
		return m.ProofMode
	}
	return ProofModeNone
}

func ProofModeEnabled(mode string) bool {
	return mode != "" && mode != ProofModeNone
}

// SupportsEndpoint checks if the model supports the given endpoint.
func (m PublicModel) SupportsEndpoint(endpoint string) bool {
	switch endpoint {
	case EndpointChatCompletions:
		return m.SupportsChatCompletions
	default:
		return false
	}
}

// SupportsStreamForEndpoint checks if the model supports streaming for the given endpoint.
func (m PublicModel) SupportsStreamForEndpoint(endpoint string) bool {
	switch endpoint {
	case EndpointChatCompletions:
		return m.SupportsChatCompletionsStream
	default:
		return false
	}
}
