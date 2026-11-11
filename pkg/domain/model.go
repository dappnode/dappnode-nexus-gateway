package domain

import (
	"encoding/json"
	"strings"

	"github.com/shopspring/decimal"
)

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
	ServiceTier       *string
	ProviderConfig    ProviderConfig
}

// PublicModel is the fully-resolved model exposed by the gateway.
type PublicModel struct {
	ID                      string
	PublicModelID           string
	DisplayName             string
	Description             *string
	ProviderModelID         string
	UpstreamModelName       string
	ServiceTier             *string
	ProviderConfig          ProviderConfig
	Fallback                *ProviderTarget
	SupportsChatCompletions bool // Routing: whether chat is served.
	// SupportsImageGenerations marks an image model, priced per image.
	SupportsImageGenerations bool
	// Capabilities is the operator's description of the model, a JSON object
	// published unchanged in /v1/models. Gateway never rejects requests by it.
	Capabilities              json.RawMessage
	ProofMode                 string
	MaxContextWindow          int
	MaxOutputTokens           int
	InputPricePerMillion      decimal.Decimal
	OutputPricePerMillion     decimal.Decimal
	CacheReadPricePerMillion  *decimal.Decimal
	CacheWritePricePerMillion *decimal.Decimal
	// PricePerImage is set on image models, in Currency.
	PricePerImage *decimal.Decimal
	Currency      string
	Active        bool
	// GuestAllowed marks a model guest chat keys may call; they can call no
	// other model or router.
	GuestAllowed bool
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
	// ponytail: the provider selects the wire protocol; add capability storage
	// when one provider needs to serve multiple protocols.
	if m.ProviderConfig.ProviderName == "typesafe" {
		return endpoint == EndpointSystemOne
	}
	switch endpoint {
	case EndpointChatCompletions:
		return m.SupportsChatCompletions
	case EndpointImageGenerations:
		return m.SupportsImageGenerations && m.PricePerImage != nil
	default:
		return false
	}
}

// SupportsImageEdits is restricted to the implemented MiniMax editing protocol.
func (m PublicModel) SupportsImageEdits() bool {
	return m.SupportsImageGenerations && strings.EqualFold(m.ProviderConfig.ProviderName, "minimax") &&
		(m.UpstreamModelName == "MiniMax-H3-Image" || m.UpstreamModelName == "MiniMax-H3-Image-Highspeed")
}
