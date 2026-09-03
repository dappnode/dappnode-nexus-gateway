package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/internal/enclavebundle"
)

func TestLoadPayloadReleasesEncryptedEnvironment(t *testing.T) {
	now := time.Now().UTC()
	revision := "0123456789abcdef0123456789abcdef01234567"
	region := "eu-central-1"
	keyARN := "arn:aws:kms:eu-central-1:111122223333:key/12345678-1234-1234-1234-123456789012"
	dataKey := bytes.Repeat([]byte{9}, 32)
	bundle := enclavebundle.SecretBundle{
		SchemaVersion:  enclavebundle.SchemaVersion,
		SourceRevision: revision,
		IssuedAt:       now.Add(-time.Minute),
		Environment: map[string]string{
			"METERING_URL":   "https://metering.example.com",
			"METERING_TOKEN": "secret-token",
		},
	}
	envelope, err := enclavebundle.Encrypt(bundle, dataKey, keyARN, region)
	if err != nil {
		t.Fatal(err)
	}
	envelope.EncryptedDataKey = base64.StdEncoding.EncodeToString([]byte("kms-wrapped-key"))
	request := enclavebundle.BootstrapRequest{
		SchemaVersion: enclavebundle.BootstrapSchemaVersion,
		Credentials: enclavebundle.AWSCredentials{
			AccessKeyID:     "ASIAEXAMPLE12345678",
			SecretAccessKey: "example-secret-access-key-value",
			SessionToken:    "example-session-token-value",
			ExpiresAt:       now.Add(time.Hour),
		},
		Envelope: envelope,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	err = loadPayload(
		context.Background(),
		payload,
		now,
		keyARN,
		region,
		revision,
		func(context.Context, enclavebundle.AWSCredentials, enclavebundle.Envelope) ([]byte, error) {
			return append([]byte(nil), dataKey...), nil
		},
		func(key, value string) error {
			got[key] = value
			return nil
		},
	)
	if err != nil {
		t.Fatalf("loadPayload: %v", err)
	}
	if got["METERING_TOKEN"] != "secret-token" {
		t.Fatal("decrypted environment was not applied")
	}
}

func TestLoadPayloadRejectsUnknownOuterFields(t *testing.T) {
	payload := []byte(`{"schema_version":1,"unexpected":true}`)
	err := loadPayload(
		context.Background(),
		payload,
		time.Now(),
		"key",
		"region",
		"revision",
		func(context.Context, enclavebundle.AWSCredentials, enclavebundle.Envelope) ([]byte, error) {
			return nil, nil
		},
		func(string, string) error { return nil },
	)
	if err == nil {
		t.Fatal("unknown bootstrap field was accepted")
	}
}
