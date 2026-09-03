package handlers_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/handlers"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/profile"
)

type recordingAttester struct {
	document  []byte
	err       error
	calls     int
	nonce     []byte
	userData  []byte
	publicKey []byte
}

func (a *recordingAttester) Attest(_ context.Context, nonce, userData, publicKey []byte) ([]byte, error) {
	a.calls++
	a.nonce = append([]byte(nil), nonce...)
	a.userData = append([]byte(nil), userData...)
	a.publicKey = append([]byte(nil), publicKey...)
	return append([]byte(nil), a.document...), a.err
}

func TestAttestationHandler_AcceptsOnlyRawBase64URL32ByteNonce(t *testing.T) {
	nonce := bytes.Repeat([]byte{0xfb}, 32)
	validNonce := base64.RawURLEncoding.EncodeToString(nonce)

	tests := []struct {
		name string
		body string
	}{
		{name: "missing", body: `{}`},
		{name: "too short", body: `{"nonce":"` + base64.RawURLEncoding.EncodeToString(nonce[:31]) + `"}`},
		{name: "too long", body: `{"nonce":"` + base64.RawURLEncoding.EncodeToString(append(nonce, 0xfb)) + `"}`},
		{name: "padded", body: `{"nonce":"` + base64.URLEncoding.EncodeToString(nonce) + `"}`},
		{name: "standard base64 alphabet", body: `{"nonce":"` + base64.RawStdEncoding.EncodeToString(nonce) + `"}`},
		{name: "malformed", body: `{"nonce":"not-base64url"}`},
		{name: "unknown field", body: `{"nonce":"` + validNonce + `","extra":true}`},
		{name: "multiple values", body: `{"nonce":"` + validNonce + `"} {}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attester := &recordingAttester{document: []byte("attestation-document")}
			handler := handlers.NewAttestationHandler(attester, make([]byte, 32), &mockLogger{})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/attestation", strings.NewReader(tt.body))

			handler.Handle(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			if attester.calls != 0 {
				t.Fatalf("attester calls = %d, want 0", attester.calls)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestAttestationHandler_PassesNonceAndManifestDigestToNSM(t *testing.T) {
	nonce := bytes.Repeat([]byte{0x42}, 32)
	document := []byte("signed-cose-document")
	publicKey := bytes.Repeat([]byte{0x33}, 32)
	attester := &recordingAttester{document: document}
	handler := handlers.NewAttestationHandler(attester, publicKey, &mockLogger{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/attestation", strings.NewReader(
		`{"nonce":"`+base64.RawURLEncoding.EncodeToString(nonce)+`"}`,
	))

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if attester.calls != 1 {
		t.Fatalf("attester calls = %d, want 1", attester.calls)
	}
	if !bytes.Equal(attester.nonce, nonce) {
		t.Fatalf("attester nonce = %x, want %x", attester.nonce, nonce)
	}
	if !bytes.Equal(attester.userData, profile.ManifestDigest()) {
		t.Fatalf("attester user data = %x, want manifest digest %x", attester.userData, profile.ManifestDigest())
	}
	if !bytes.Equal(attester.publicKey, publicKey) {
		t.Fatalf("attester public key = %x, want %x", attester.publicKey, publicKey)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var response struct {
		Document        string          `json:"document"`
		Format          string          `json:"format"`
		Encoding        string          `json:"encoding"`
		UserData        string          `json:"user_data"`
		HPKEPublicKey   string          `json:"hpke_public_key"`
		HPKEKeyEncoding string          `json:"hpke_public_key_encoding"`
		Manifest        json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Document != base64.StdEncoding.EncodeToString(document) {
		t.Fatalf("document = %q, want encoded NSM document", response.Document)
	}
	if response.Format != "aws-nitro-cose-sign1" || response.Encoding != "base64" {
		t.Fatalf("unexpected document metadata: format=%q encoding=%q", response.Format, response.Encoding)
	}
	if response.UserData != hex.EncodeToString(profile.ManifestDigest()) {
		t.Fatalf("user_data = %q, want manifest digest", response.UserData)
	}
	if response.HPKEPublicKey != hex.EncodeToString(publicKey) || response.HPKEKeyEncoding != "raw-x25519-hex" {
		t.Fatalf("unexpected HPKE key metadata: key=%q encoding=%q", response.HPKEPublicKey, response.HPKEKeyEncoding)
	}
	if !bytes.Equal(response.Manifest, profile.ManifestJSON()) {
		t.Fatalf("manifest = %s, want %s", response.Manifest, profile.ManifestJSON())
	}
}

func TestAttestationHandler_NSMFailureIsUnavailableAndNeverCacheable(t *testing.T) {
	attester := &recordingAttester{err: errors.New("NSM unavailable")}
	handler := handlers.NewAttestationHandler(attester, make([]byte, 32), &mockLogger{})
	recorder := httptest.NewRecorder()
	nonce := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	request := httptest.NewRequest(http.MethodPost, "/v1/attestation", strings.NewReader(`{"nonce":"`+nonce+`"}`))

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if attester.calls != 1 {
		t.Fatalf("attester calls = %d, want 1", attester.calls)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
