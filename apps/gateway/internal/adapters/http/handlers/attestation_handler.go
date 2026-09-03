package handlers

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/profile"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

const (
	attestationRequestLimit   = 1024
	attestationNonceSize      = 32
	maxConcurrentAttestations = 4
)

type AttestationHandler struct {
	attester      ports.Attester
	hpkePublicKey []byte
	logger        ports.Logger
	limit         chan struct{}
}

func NewAttestationHandler(attester ports.Attester, hpkePublicKey []byte, logger ports.Logger) *AttestationHandler {
	return &AttestationHandler{
		attester:      attester,
		hpkePublicKey: append([]byte(nil), hpkePublicKey...),
		logger:        logger,
		limit:         make(chan struct{}, maxConcurrentAttestations),
	}
}

func (h *AttestationHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	var requestBody struct {
		Nonce string `json:"nonce"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, attestationRequestLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&requestBody); err != nil {
		WriteError(w, domain.ErrInvalidField("invalid attestation request"))
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		WriteError(w, domain.ErrInvalidField("invalid attestation request"))
		return
	}

	nonce, err := decodeAttestationNonce(requestBody.Nonce)
	if err != nil {
		WriteError(w, domain.ErrInvalidField("nonce must be exactly 32 random bytes encoded as base64url without padding"))
		return
	}

	select {
	case h.limit <- struct{}{}:
		defer func() { <-h.limit }()
	default:
		metrics.AttestationOperationsTotal.WithLabelValues("rate_limited").Inc()
		WriteError(w, domain.ErrProviderUnavailable("Nitro attestation"))
		return
	}

	start := time.Now()
	document, err := h.attester.Attest(r.Context(), nonce, profile.ManifestDigest(), h.hpkePublicKey)
	if err != nil {
		metrics.AttestationOperationsTotal.WithLabelValues("error").Inc()
		metrics.AttestationDuration.Observe(time.Since(start).Seconds())
		h.logger.Error("Nitro attestation failed", "error", err)
		WriteError(w, domain.ErrProviderUnavailable("Nitro attestation"))
		return
	}
	metrics.AttestationOperationsTotal.WithLabelValues("success").Inc()
	metrics.AttestationDuration.Observe(time.Since(start).Seconds())

	digest := profile.ManifestDigest()
	WriteJSON(w, http.StatusOK, struct {
		Document         string          `json:"document"`
		Format           string          `json:"format"`
		Encoding         string          `json:"encoding"`
		UserData         string          `json:"user_data"`
		UserDataEncoding string          `json:"user_data_encoding"`
		HPKEPublicKey    string          `json:"hpke_public_key"`
		HPKEKeyEncoding  string          `json:"hpke_public_key_encoding"`
		Manifest         json.RawMessage `json:"manifest"`
	}{
		Document:         base64.StdEncoding.EncodeToString(document),
		Format:           "aws-nitro-cose-sign1",
		Encoding:         "base64",
		UserData:         hex.EncodeToString(digest),
		UserDataEncoding: "sha384-hex",
		HPKEPublicKey:    hex.EncodeToString(h.hpkePublicKey),
		HPKEKeyEncoding:  "raw-x25519-hex",
		Manifest:         json.RawMessage(profile.ManifestJSON()),
	})
}

func decodeAttestationNonce(encoded string) ([]byte, error) {
	nonce, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(nonce) != attestationNonceSize {
		return nil, errors.New("invalid nonce")
	}
	return nonce, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}
