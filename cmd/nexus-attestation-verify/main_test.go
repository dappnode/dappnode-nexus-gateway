package main

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anchorageoss/awsnitroverifier"
)

type fakeVerifier struct {
	result *awsnitroverifier.ValidationResult
	err    error
}

func (v fakeVerifier) Validate([]byte) (*awsnitroverifier.ValidationResult, error) {
	return v.result, v.err
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestValidateAttestation(t *testing.T) {
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)

	t.Run("accepts a fully bound production attestation", func(t *testing.T) {
		fetched, nonce, pcrs, result := validFixture(now)
		summary, err := validateAttestation(fetched, nonce, pcrs, now, 2*time.Minute, 30*time.Second, fakeVerifier{result: result})
		if err != nil {
			t.Fatalf("validateAttestation() error = %v", err)
		}
		if !summary.Verified {
			t.Fatal("summary.Verified = false")
		}
		wantDigest := sha512.Sum384(fetched.manifest)
		if summary.ManifestSHA384 != hex.EncodeToString(wantDigest[:]) {
			t.Fatalf("ManifestSHA384 = %q, want %x", summary.ManifestSHA384, wantDigest)
		}
		if !bytes.Equal(summary.Manifest, fetched.manifest) {
			t.Fatalf("Manifest = %s, want %s", summary.Manifest, fetched.manifest)
		}
		if summary.HPKEPublicKey != hex.EncodeToString(fetched.hpkePublicKey) {
			t.Fatalf("HPKEPublicKey = %q, want %x", summary.HPKEPublicKey, fetched.hpkePublicKey)
		}
	})

	tests := []struct {
		name    string
		mutate  func(*fetchedAttestation, []byte, map[uint][]byte, *awsnitroverifier.ValidationResult)
		wantErr string
	}{
		{
			name: "untrusted chain",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.ChainTrusted = false
			},
			wantErr: "not rooted",
		},
		{
			name: "wrong root fingerprint",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.RootFingerprint = strings.Repeat("a", 64)
			},
			wantErr: "not rooted",
		},
		{
			name: "invalid signature or certificate",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.Valid = false
				result.Errors = []error{errors.New("signature verification failed")}
			},
			wantErr: "signature verification failed",
		},
		{
			name: "nonce mismatch",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.Nonce[0] ^= 0xff
			},
			wantErr: "nonce does not match",
		},
		{
			name: "signed manifest digest mismatch",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.UserData[0] ^= 0xff
			},
			wantErr: "signed user_data",
		},
		{
			name: "response manifest digest mismatch",
			mutate: func(fetched *fetchedAttestation, _ []byte, _ map[uint][]byte, _ *awsnitroverifier.ValidationResult) {
				fetched.userData[0] ^= 0xff
			},
			wantErr: "response user_data",
		},
		{
			name: "signed HPKE key mismatch",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.PublicKey[0] ^= 0xff
			},
			wantErr: "signed HPKE public key",
		},
		{
			name: "decoded document HPKE key mismatch",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.Document.PublicKey[0] ^= 0xff
			},
			wantErr: "document HPKE public key",
		},
		{
			name: "stale timestamp",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.Document.Timestamp = uint64(now.Add(-3 * time.Minute).UnixMilli())
			},
			wantErr: "timestamp is stale",
		},
		{
			name: "future timestamp",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.Document.Timestamp = uint64(now.Add(time.Minute).UnixMilli())
			},
			wantErr: "too far in the future",
		},
		{
			name: "debug PCR",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.Document.PCRs[1] = make([]byte, pcrSize)
			},
			wantErr: "all zero",
		},
		{
			name: "PCR mismatch",
			mutate: func(_ *fetchedAttestation, _ []byte, _ map[uint][]byte, result *awsnitroverifier.ValidationResult) {
				result.Document.PCRs[2][0] ^= 0xff
			},
			wantErr: "does not match",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fetched, nonce, pcrs, result := validFixture(now)
			test.mutate(fetched, nonce, pcrs, result)
			_, err := validateAttestation(fetched, nonce, pcrs, now, 2*time.Minute, 30*time.Second, fakeVerifier{result: result})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateAttestation() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestFetchAttestation(t *testing.T) {
	nonce := bytes.Repeat([]byte{0x42}, nonceSize)
	manifest := json.RawMessage(`{"schema_version":1,"profile":"nexus-gateway-m1"}`)
	digest := sha512.Sum384(manifest)
	document := []byte{0xd2, 0x84, 0x43, 0xa1}
	hpkePublicKey := bytes.Repeat([]byte{0x44}, 32)

	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		var requestPayload struct {
			Nonce string `json:"nonce"`
		}
		if err := json.NewDecoder(request.Body).Decode(&requestPayload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if requestPayload.Nonce != base64.RawURLEncoding.EncodeToString(nonce) {
			t.Fatalf("nonce = %q", requestPayload.Nonce)
		}

		responsePayload, err := json.Marshal(endpointResponse{
			Document:         base64.StdEncoding.EncodeToString(document),
			Format:           "aws-nitro-cose-sign1",
			Encoding:         "base64",
			UserData:         hex.EncodeToString(digest[:]),
			UserDataEncoding: "sha384-hex",
			HPKEPublicKey:    hex.EncodeToString(hpkePublicKey),
			HPKEKeyEncoding:  "raw-x25519-hex",
			Manifest:         manifest,
		})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(responsePayload)),
		}, nil
	})

	got, err := fetchAttestation(context.Background(), client, "https://gateway.example/v1/attestation", nonce)
	if err != nil {
		t.Fatalf("fetchAttestation() error = %v", err)
	}
	if !bytes.Equal(got.document, document) {
		t.Fatalf("document = %x, want %x", got.document, document)
	}
	if !bytes.Equal(got.manifest, manifest) {
		t.Fatalf("manifest = %s, want %s", got.manifest, manifest)
	}
	if !bytes.Equal(got.userData, digest[:]) {
		t.Fatalf("userData = %x, want %x", got.userData, digest)
	}
	if !bytes.Equal(got.hpkePublicKey, hpkePublicKey) {
		t.Fatalf("hpkePublicKey = %x, want %x", got.hpkePublicKey, hpkePublicKey)
	}
}

func TestFetchAttestationRejectsUnsafeResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantErr     string
	}{
		{name: "HTTP error", status: http.StatusBadGateway, contentType: "application/json", body: `{}`, wantErr: "HTTP 502"},
		{name: "wrong media type", status: http.StatusOK, contentType: "text/html", body: `{}`, wantErr: "non-JSON"},
		{name: "unknown field", status: http.StatusOK, contentType: "application/json", body: `{"unknown":true}`, wantErr: "unknown field"},
		{name: "oversized", status: http.StatusOK, contentType: "application/json", body: strings.Repeat("x", maxResponseSize+1), wantErr: "exceeds"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := doerFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.status,
					Header:     http.Header{"Content-Type": []string{test.contentType}},
					Body:       io.NopCloser(strings.NewReader(test.body)),
				}, nil
			})
			_, err := fetchAttestation(context.Background(), client, "https://gateway.example/v1/attestation", make([]byte, nonceSize))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("fetchAttestation() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestParseFlagsRequiresHTTPSAndProductionPCRs(t *testing.T) {
	validPCR := hex.EncodeToString(bytes.Repeat([]byte{0x11}, pcrSize))
	validArgs := []string{
		"--url", "https://gateway.example/v1/attestation",
		"--pcr0", validPCR,
		"--pcr1", validPCR,
		"--pcr2", validPCR,
	}
	if _, err := parseFlags(validArgs, io.Discard); err != nil {
		t.Fatalf("parseFlags(valid) error = %v", err)
	}

	badURL := append([]string(nil), validArgs...)
	badURL[1] = "http://gateway.example/v1/attestation"
	if _, err := parseFlags(badURL, io.Discard); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("parseFlags(http) error = %v, want HTTPS error", err)
	}

	zeroPCR := append([]string(nil), validArgs...)
	zeroPCR[3] = strings.Repeat("0", pcrSize*2)
	if _, err := parseFlags(zeroPCR, io.Discard); err == nil || !strings.Contains(err.Error(), "all zero") {
		t.Fatalf("parseFlags(zero PCR) error = %v, want all-zero error", err)
	}
}

func TestRunHelpIsSuccessful(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if status := run([]string{"--help"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(--help) status = %d, stderr = %q", status, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage: nexus-attestation-verify") {
		t.Fatalf("help output = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "error:") {
		t.Fatalf("help output contains an error: %q", stderr.String())
	}
}

func validFixture(now time.Time) (*fetchedAttestation, []byte, map[uint][]byte, *awsnitroverifier.ValidationResult) {
	manifest := json.RawMessage(`{"schema_version":1,"profile":"nexus-gateway-m1"}`)
	digest := sha512.Sum384(manifest)
	nonce := bytes.Repeat([]byte{0x31}, nonceSize)
	pcrs := map[uint][]byte{
		0: bytes.Repeat([]byte{0x10}, pcrSize),
		1: bytes.Repeat([]byte{0x11}, pcrSize),
		2: bytes.Repeat([]byte{0x12}, pcrSize),
	}
	documentPCRs := map[uint][]byte{
		0: append([]byte(nil), pcrs[0]...),
		1: append([]byte(nil), pcrs[1]...),
		2: append([]byte(nil), pcrs[2]...),
	}
	hpkePublicKey := bytes.Repeat([]byte{0x41}, 32)
	return &fetchedAttestation{
			document:      []byte("signed document"),
			manifest:      append(json.RawMessage(nil), manifest...),
			userData:      append([]byte(nil), digest[:]...),
			hpkePublicKey: append([]byte(nil), hpkePublicKey...),
		}, append([]byte(nil), nonce...), pcrs, &awsnitroverifier.ValidationResult{
			Valid:           true,
			ChainTrusted:    true,
			RootFingerprint: expectedAWSRootFingerprint,
			UserData:        append([]byte(nil), digest[:]...),
			PublicKey:       append([]byte(nil), hpkePublicKey...),
			Nonce:           append([]byte(nil), nonce...),
			Document: &awsnitroverifier.AttestationDocument{
				Timestamp: uint64(now.Add(-time.Second).UnixMilli()),
				PCRs:      documentPCRs,
				PublicKey: append([]byte(nil), hpkePublicKey...),
			},
		}
}
