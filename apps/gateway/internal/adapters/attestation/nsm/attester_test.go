//go:build linux

package nsmattestation

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/0xsequence/nsm/request"
	"github.com/0xsequence/nsm/response"
)

type fakeSession struct {
	request  request.Request
	response response.Response
	err      error
}

func (s *fakeSession) Send(req request.Request) (response.Response, error) {
	s.request = req
	return s.response, s.err
}

func (s *fakeSession) Close() error { return nil }

func TestAttester_AttestSendsNonceAndManifestDigest(t *testing.T) {
	document := []byte("cose-sign1")
	session := &fakeSession{response: response.Response{
		Attestation: &response.Attestation{Document: document},
	}}
	attester := &Attester{session: session}
	nonce := bytes.Repeat([]byte{0x11}, 32)
	manifestDigest := bytes.Repeat([]byte{0x22}, 48)
	publicKey := bytes.Repeat([]byte{0x33}, 32)

	got, err := attester.Attest(context.Background(), nonce, manifestDigest, publicKey)
	if err != nil {
		t.Fatalf("Attest returned error: %v", err)
	}
	if !bytes.Equal(got, document) {
		t.Fatalf("document = %x, want %x", got, document)
	}

	req, ok := session.request.(*request.Attestation)
	if !ok {
		t.Fatalf("NSM request type = %T, want *request.Attestation", session.request)
	}
	if !bytes.Equal(req.Nonce, nonce) {
		t.Fatalf("request nonce = %x, want %x", req.Nonce, nonce)
	}
	if !bytes.Equal(req.UserData, manifestDigest) {
		t.Fatalf("request user data = %x, want %x", req.UserData, manifestDigest)
	}
	if !bytes.Equal(req.PublicKey, publicKey) {
		t.Fatalf("request public key = %x, want %x", req.PublicKey, publicKey)
	}
}

func TestAttester_AttestReportsSessionAndNSMErrors(t *testing.T) {
	tests := []struct {
		name       string
		response   response.Response
		sessionErr error
		want       string
	}{
		{
			name:       "session send failure",
			sessionErr: errors.New("ioctl failed"),
			want:       "request Nitro attestation",
		},
		{
			name:     "NSM rejection",
			response: response.Response{Error: response.ECInvalidArgument},
			want:     "Nitro attestation rejected: InvalidArgument",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &fakeSession{response: tt.response, err: tt.sessionErr}
			attester := &Attester{session: session}

			document, err := attester.Attest(context.Background(), make([]byte, 32), make([]byte, 48), make([]byte, 32))

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
			if document != nil {
				t.Fatalf("document = %x, want nil", document)
			}
			if session.request == nil {
				t.Fatal("expected an NSM request")
			}
		})
	}
}

func TestAttester_AttestRejectsMissingDocument(t *testing.T) {
	tests := []struct {
		name     string
		response response.Response
	}{
		{name: "nil attestation", response: response.Response{}},
		{name: "empty document", response: response.Response{Attestation: &response.Attestation{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attester := &Attester{session: &fakeSession{response: tt.response}}

			document, err := attester.Attest(context.Background(), make([]byte, 32), make([]byte, 48), make([]byte, 32))

			if err == nil || !strings.Contains(err.Error(), "returned no attestation document") {
				t.Fatalf("error = %v, want missing document error", err)
			}
			if document != nil {
				t.Fatalf("document = %x, want nil", document)
			}
		})
	}
}
