package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// TinfoilHandler serves safe Tinfoil attested-transport proof APIs.
type TinfoilHandler struct {
	auth   ports.AuthService
	proofs ports.TinfoilTransportProofRepository
	logger ports.Logger
}

func NewTinfoilHandler(auth ports.AuthService, proofs ports.TinfoilTransportProofRepository, logger ports.Logger) *TinfoilHandler {
	return &TinfoilHandler{auth: auth, proofs: proofs, logger: logger}
}

func (h *TinfoilHandler) HandleGetProof(w http.ResponseWriter, r *http.Request) {
	authCtx, err := h.authenticate(r)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}
	responseID := r.PathValue("response_id")
	if responseID == "" {
		WriteErrorWithLog(w, r, h.logger, domain.ErrInvalidField("response_id is required"))
		return
	}
	if h.proofs == nil {
		WriteErrorWithLog(w, r, h.logger, domain.ErrInternal("an internal error occurred").WithMeta(
			"reason", "Tinfoil proof repository is not configured",
		))
		return
	}
	proof, err := h.proofs.GetTinfoilTransportProof(r.Context(), authCtx.Account.ID, responseID)
	if err != nil {
		WriteErrorWithLog(w, r, h.logger, err)
		return
	}
	WriteJSON(w, http.StatusOK, tinfoilProofResponseFromDomain(proof))
}

func (h *TinfoilHandler) authenticate(r *http.Request) (domain.AuthContext, error) {
	token, err := ExtractBearerToken(r)
	if err != nil {
		return domain.AuthContext{}, err
	}
	return h.auth.AuthenticateAPIKey(r.Context(), token)
}

type tinfoilProofResponse struct {
	Provider             string          `json:"provider"`
	PublicModelID        string          `json:"public_model_id"`
	UpstreamModelID      string          `json:"upstream_model_id"`
	ProviderResponseID   string          `json:"provider_response_id"`
	EnclaveHost          *string         `json:"enclave_host"`
	ConfigRepo           *string         `json:"config_repo"`
	Digest               *string         `json:"digest"`
	CodeFingerprint      *string         `json:"code_fingerprint"`
	EnclaveFingerprint   *string         `json:"enclave_fingerprint"`
	TLSPublicKey         *string         `json:"tls_public_key"`
	HPKEPublicKey        *string         `json:"hpke_public_key"`
	TransportMode        *string         `json:"transport_mode"`
	SDKVersion           *string         `json:"sdk_version"`
	Status               string          `json:"status"`
	FailureReason        *string         `json:"failure_reason"`
	VerificationEvidence json.RawMessage `json:"verification_evidence,omitempty"`
	CreatedAt            string          `json:"created_at"`
	VerifiedAt           *string         `json:"verified_at"`
}

func tinfoilProofResponseFromDomain(proof domain.TinfoilTransportProof) tinfoilProofResponse {
	var verifiedAt *string
	if proof.VerifiedAt != nil {
		v := proof.VerifiedAt.Format(http.TimeFormat)
		verifiedAt = &v
	}
	return tinfoilProofResponse{
		Provider:             proof.Provider,
		PublicModelID:        proof.PublicModelID,
		UpstreamModelID:      proof.UpstreamModelID,
		ProviderResponseID:   proof.ProviderResponseID,
		EnclaveHost:          proof.EnclaveHost,
		ConfigRepo:           proof.ConfigRepo,
		Digest:               proof.Digest,
		CodeFingerprint:      proof.CodeFingerprint,
		EnclaveFingerprint:   proof.EnclaveFingerprint,
		TLSPublicKey:         proof.TLSPublicKey,
		HPKEPublicKey:        proof.HPKEPublicKey,
		TransportMode:        proof.TransportMode,
		SDKVersion:           proof.SDKVersion,
		Status:               proof.Status,
		FailureReason:        proof.FailureReason,
		VerificationEvidence: proof.VerificationEvidenceJSON,
		CreatedAt:            proof.CreatedAt.Format(http.TimeFormat),
		VerifiedAt:           verifiedAt,
	}
}
