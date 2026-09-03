package enclavebundle

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

const (
	testRevision = "0123456789abcdef0123456789abcdef01234567"
	testRegion   = "eu-central-1"
	testKeyARN   = "arn:aws:kms:eu-central-1:111122223333:key/12345678-1234-1234-1234-123456789012"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	bundle := SecretBundle{
		SchemaVersion:  SchemaVersion,
		SourceRevision: testRevision,
		IssuedAt:       now.Add(-time.Minute),
		Environment: map[string]string{
			"METERING_URL":   "https://metering.example.com",
			"METERING_TOKEN": "metering-secret",
			"OPENAI_API_KEY": "provider-secret",
		},
	}
	envelope, err := Encrypt(bundle, key, testKeyARN, testRegion)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	envelope.EncryptedDataKey = base64.StdEncoding.EncodeToString([]byte("kms-ciphertext"))
	got, err := Decrypt(envelope, key, testKeyARN, testRegion, testRevision, now)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got.Environment["METERING_TOKEN"] != bundle.Environment["METERING_TOKEN"] {
		t.Fatal("decrypted bundle does not match input")
	}
}

func TestDecryptRejectsTampering(t *testing.T) {
	now := time.Now().UTC()
	key := bytes.Repeat([]byte{7}, 32)
	bundle := SecretBundle{
		SchemaVersion:  SchemaVersion,
		SourceRevision: testRevision,
		IssuedAt:       now.Add(-time.Minute),
		Environment: map[string]string{
			"METERING_URL":   "https://metering.example.com",
			"METERING_TOKEN": "secret",
		},
	}
	envelope, err := Encrypt(bundle, key, testKeyARN, testRegion)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, _ := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	ciphertext[len(ciphertext)-1] ^= 1
	envelope.Ciphertext = base64.StdEncoding.EncodeToString(ciphertext)
	if _, err := Decrypt(envelope, key, testKeyARN, testRegion, testRevision, now); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestValidateSecretBundleRejectsUntrustedConfiguration(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name        string
		environment map[string]string
	}{
		{
			name: "plaintext metering",
			environment: map[string]string{
				"METERING_URL":   "http://metering.example.com",
				"METERING_TOKEN": "secret",
			},
		},
		{
			name: "arbitrary process setting",
			environment: map[string]string{
				"METERING_URL":   "https://metering.example.com",
				"METERING_TOKEN": "secret",
				"GODEBUG":        "http2debug=2",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := SecretBundle{
				SchemaVersion:  SchemaVersion,
				SourceRevision: testRevision,
				IssuedAt:       now.Add(-time.Minute),
				Environment:    test.environment,
			}
			if err := ValidateSecretBundle(bundle, testRevision, now); err == nil {
				t.Fatal("untrusted configuration was accepted")
			}
		})
	}
}
