package profile

import (
	"bytes"
	"crypto/sha512"
	"encoding/json"
	"testing"
)

func TestManifestDigestCoversBuildPolicy(t *testing.T) {
	restore := setTestBuildValues()
	defer restore()

	manifestJSON := ManifestJSON()
	var decoded map[string]any
	if err := json.Unmarshal(manifestJSON, &decoded); err != nil {
		t.Fatalf("ManifestJSON is invalid: %v", err)
	}
	secretRelease, ok := decoded["secret_release"].(map[string]any)
	if !ok || decoded["source_revision"] != SourceRevision ||
		secretRelease["aws_region"] != AWSRegion ||
		secretRelease["kms_key_arn"] != KMSKeyARN {
		t.Fatalf("manifest does not contain measured build values: %s", manifestJSON)
	}
	egress, ok := decoded["egress"].(map[string]any)
	if !ok || egress["parent_cid"] != float64(ParentCID) {
		t.Fatalf("manifest does not declare the Nitro parent CID: %s", manifestJSON)
	}
	routes, ok := egress["provider_routes"].([]any)
	if !ok || len(routes) != len(ProviderEgressRoutes()) {
		t.Fatalf("manifest does not declare every provider egress route: %s", manifestJSON)
	}
	if decoded["schema_version"] != float64(ManifestSchema) || decoded["profile"] != "nexus-gateway-v2" {
		t.Fatalf("manifest does not identify the v2 confidential profile: %s", manifestJSON)
	}
	ingress, ok := decoded["ingress"].(map[string]any)
	if !ok {
		t.Fatalf("manifest does not declare ingress: %s", manifestJSON)
	}
	e2ee, ok := ingress["e2ee"].(map[string]any)
	if !ok || e2ee["protocol"] != "ehbp-v1" ||
		e2ee["suite"] != "DHKEM-X25519-HKDF-SHA256/HKDF-SHA256/AES-256-GCM" ||
		e2ee["endpoint"] != "/v1/confidential/chat/completions" ||
		e2ee["request_encrypted"] != true || e2ee["response_encrypted"] != true {
		t.Fatalf("manifest does not pin the confidential transport: %s", manifestJSON)
	}

	expected := sha512.Sum384(manifestJSON)
	if !bytes.Equal(ManifestDigest(), expected[:]) {
		t.Fatal("ManifestDigest does not hash the exact returned manifest")
	}
}

func TestValidateBuild(t *testing.T) {
	restore := setTestBuildValues()
	defer restore()
	if err := ValidateBuild(); err != nil {
		t.Fatalf("valid build rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func()
	}{
		{name: "unknown revision", mutate: func() { SourceRevision = "unknown" }},
		{name: "abbreviated revision", mutate: func() { SourceRevision = "0123456" }},
		{name: "invalid region", mutate: func() { AWSRegion = "local" }},
		{name: "KMS alias instead of key ARN", mutate: func() { KMSKeyARN = "alias/nexus" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			restoreCase := setTestBuildValues()
			defer restoreCase()
			test.mutate()
			if err := ValidateBuild(); err == nil {
				t.Fatal("invalid build was accepted")
			}
		})
	}
}

func setTestBuildValues() func() {
	previousRevision := SourceRevision
	previousRegion := AWSRegion
	previousKMSKeyARN := KMSKeyARN
	SourceRevision = "0123456789abcdef0123456789abcdef01234567"
	AWSRegion = "eu-central-1"
	KMSKeyARN = "arn:aws:kms:eu-central-1:111122223333:key/12345678-1234-1234-1234-123456789012"
	return func() {
		SourceRevision = previousRevision
		AWSRegion = previousRegion
		KMSKeyARN = previousKMSKeyARN
	}
}
