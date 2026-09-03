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

// TinfoilProofListParams describes a user-facing proof history query.
type TinfoilProofListParams struct {
	Offset int
	Limit  int
	Status string
	Query  string
}

// TinfoilTransportProofRecord enriches a proof with safe API key context for
// dashboard history views. It never contains raw API key material.
type TinfoilTransportProofRecord struct {
	Proof        TinfoilTransportProof
	APIKeyName   *string
	APIKeyPrefix *string
}
