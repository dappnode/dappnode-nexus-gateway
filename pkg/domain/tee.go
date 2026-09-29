package domain

import (
	"encoding/json"
	"time"
)

const (
	ProofStatusVerified     = "verified"
	ProofStatusFailed       = "failed"
	ProofStatusNotAvailable = "not_available"
	ProofStatusUnsupported  = "unsupported"
)

// TinfoilTransportProof is the permanent, safe proof record for one response
// generated over Tinfoil's attested encrypted transport. It contains no raw
// prompt, decrypted request body, raw response, or decrypted response body.
type TinfoilTransportProof struct {
	ID                       string
	AccountID                string
	APIKeyID                 string
	Provider                 string
	PublicModelID            string
	UpstreamModelID          string
	ProviderResponseID       string
	EnclaveHost              *string
	ConfigRepo               *string
	Digest                   *string
	CodeFingerprint          *string
	EnclaveFingerprint       *string
	TLSPublicKey             *string
	HPKEPublicKey            *string
	TransportMode            *string
	SDKVersion               *string
	Status                   string
	FailureReason            *string
	VerificationEvidenceJSON json.RawMessage
	CreatedAt                time.Time
	VerifiedAt               *time.Time
}
