package domain

import "time"

// PublicModelEntry represents a public model as managed by the control plane.
type PublicModelEntry struct {
	ID                            string
	DisplayName                   string
	ProviderModelID               string
	Active                        bool
	Description                   *string
	MaxContextWindow              int
	MaxOutputTokens               int
	SupportsChatCompletions       bool
	SupportsChatCompletionsStream bool
	SupportsTools                 bool
	SupportsParallelToolCalls     bool
	SupportsStructuredOutput      bool
	SupportsReasoning             bool
	ProofMode                     string
	CreatedAt                     time.Time
}

func (m PublicModelEntry) EffectiveProofMode() string {
	if m.ProofMode != "" && m.ProofMode != ProofModeNone {
		return m.ProofMode
	}
	return ProofModeNone
}
