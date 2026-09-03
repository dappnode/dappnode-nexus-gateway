package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/handlers"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestTinfoilHandler_GetProofReturnsSafeEvidence(t *testing.T) {
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	value := func(v string) *string { return &v }
	proofRepo := &handlerTinfoilRepo{proofs: map[string]domain.TinfoilTransportProof{
		"cmpl-tinfoil-safe": {
			AccountID:                "acc1",
			APIKeyID:                 "key1",
			Provider:                 "tinfoil",
			PublicModelID:            "tinfoil/kimi-k2",
			UpstreamModelID:          "kimi-k2",
			ProviderResponseID:       "cmpl-tinfoil-safe",
			EnclaveHost:              value("inference.tinfoil.sh"),
			ConfigRepo:               value("tinfoilsh/confidential-model-router"),
			Digest:                   value("sha256:abc"),
			CodeFingerprint:          value("code-fp"),
			EnclaveFingerprint:       value("enclave-fp"),
			TLSPublicKey:             value("tls-key"),
			HPKEPublicKey:            value("hpke-key"),
			TransportMode:            value("ehbp"),
			SDKVersion:               value("github.com/tinfoilsh/tinfoil-go v0.13.1"),
			Status:                   domain.ProofStatusVerified,
			VerificationEvidenceJSON: json.RawMessage(`{"ground_truth":{"digest":"sha256:abc"}}`),
			CreatedAt:                now,
			VerifiedAt:               &now,
		},
	}}
	h := handlers.NewTinfoilHandler(
		&mockAuthService{authCtx: domain.AuthContext{Account: domain.Account{ID: "acc1"}}},
		proofRepo,
		&mockLogger{},
	)

	req := httptest.NewRequest("GET", "/v1/tinfoil/proofs/cmpl-tinfoil-safe", nil)
	req.SetPathValue("response_id", "cmpl-tinfoil-safe")
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()

	h.HandleGetProof(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["provider_response_id"] != "cmpl-tinfoil-safe" || body["status"] != domain.ProofStatusVerified {
		t.Fatalf("unexpected proof response: %#v", body)
	}
	if body["digest"] != "sha256:abc" || body["transport_mode"] != "ehbp" {
		t.Fatalf("missing safe Tinfoil evidence: %#v", body)
	}
	if _, ok := body["verification_evidence"].(map[string]any); !ok {
		t.Fatalf("verification evidence missing or wrong type: %#v", body["verification_evidence"])
	}
}

type handlerTinfoilRepo struct {
	proofs map[string]domain.TinfoilTransportProof
}

func (r *handlerTinfoilRepo) UpsertTinfoilTransportProof(_ context.Context, proof domain.TinfoilTransportProof) error {
	if r.proofs == nil {
		r.proofs = make(map[string]domain.TinfoilTransportProof)
	}
	r.proofs[proof.ProviderResponseID] = proof
	return nil
}

func (r *handlerTinfoilRepo) GetTinfoilTransportProof(_ context.Context, accountID, providerResponseID string) (domain.TinfoilTransportProof, error) {
	proof, ok := r.proofs[providerResponseID]
	if !ok || proof.AccountID != accountID {
		return domain.TinfoilTransportProof{}, domain.ErrNotFound("tinfoil proof", providerResponseID)
	}
	return proof, nil
}
