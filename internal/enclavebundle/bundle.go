package enclavebundle

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	SchemaVersion          = 1
	BootstrapSchemaVersion = 1
	dataKeyBytes           = 32
	maxEncryptedKeySize    = 16 * 1024
	maxCiphertextSize      = 128 * 1024
	maxEnvironmentBytes    = 64 * 1024
	maxEnvironmentValue    = 8 * 1024
)

var providerSecretPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,119}_API_KEY$`)

var allowedEnvironmentKeys = map[string]struct{}{
	"METERING_URL":                   {},
	"METERING_TOKEN":                 {},
	"ROUTER_URL":                     {},
	"LOG_LEVEL":                      {},
	"EUR_TO_USD_FALLBACK_RATE":       {},
	"PII_FILTER_ENABLED":             {},
	"PRESIDIO_ANALYZER_URL":          {},
	"PII_FILTER_LANGUAGE":            {},
	"PII_FILTER_SCORE_THRESHOLD":     {},
	"PII_FILTER_TIMEOUT_MS":          {},
	"PII_FILTER_FAIL_OPEN":           {},
	"TINFOIL_PROXY_BASE_URL":         {},
	"TINFOIL_CONFIG_REPO":            {},
	"TINFOIL_ENCLAVE_HOST":           {},
	"TINFOIL_ATTESTATION_BUNDLE_URL": {},
}

// Envelope is safe to store on the parent host. It contains only a KMS-wrapped
// data key and AES-256-GCM ciphertext, never plaintext gateway configuration.
type Envelope struct {
	SchemaVersion     int               `json:"schema_version"`
	KMSKeyARN         string            `json:"kms_key_arn"`
	AWSRegion         string            `json:"aws_region"`
	EncryptionContext map[string]string `json:"encryption_context"`
	EncryptedDataKey  string            `json:"encrypted_data_key"`
	Nonce             string            `json:"nonce"`
	Ciphertext        string            `json:"ciphertext"`
}

// SecretBundle is decrypted only in enclave memory. Environment keys are
// deliberately constrained to gateway runtime configuration.
type SecretBundle struct {
	SchemaVersion  int               `json:"schema_version"`
	SourceRevision string            `json:"source_revision"`
	IssuedAt       time.Time         `json:"issued_at"`
	ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
	Environment    map[string]string `json:"environment"`
}

// BootstrapRequest is the only plaintext message accepted from the parent.
// It contains temporary parent-role credentials and ciphertext, but no Nexus
// runtime secret values.
type BootstrapRequest struct {
	SchemaVersion int            `json:"schema_version"`
	Credentials   AWSCredentials `json:"aws_credentials"`
	Envelope      Envelope       `json:"envelope"`
}

type AWSCredentials struct {
	AccessKeyID     string    `json:"access_key_id"`
	SecretAccessKey string    `json:"secret_access_key"`
	SessionToken    string    `json:"session_token"`
	ExpiresAt       time.Time `json:"expires_at"`
}

func ValidateBootstrapRequest(request BootstrapRequest, now time.Time) error {
	if request.SchemaVersion != BootstrapSchemaVersion {
		return fmt.Errorf("unsupported bootstrap schema %d", request.SchemaVersion)
	}
	credentials := request.Credentials
	if len(credentials.AccessKeyID) < 16 || len(credentials.AccessKeyID) > 128 || strings.ContainsAny(credentials.AccessKeyID, "\r\n\x00") {
		return errors.New("bootstrap AWS access key ID is invalid")
	}
	if len(credentials.SecretAccessKey) < 16 || len(credentials.SecretAccessKey) > 256 || strings.ContainsAny(credentials.SecretAccessKey, "\r\n\x00") {
		return errors.New("bootstrap AWS secret access key is invalid")
	}
	if len(credentials.SessionToken) < 16 || len(credentials.SessionToken) > 16*1024 || strings.ContainsAny(credentials.SessionToken, "\r\n\x00") {
		return errors.New("bootstrap requires temporary AWS credentials with a session token")
	}
	if credentials.ExpiresAt.IsZero() || !credentials.ExpiresAt.After(now.Add(2*time.Minute)) {
		return errors.New("bootstrap AWS credentials are expired or too close to expiration")
	}
	return nil
}

func EncryptionContext(sourceRevision string) map[string]string {
	return map[string]string{
		"nexus:bundle-schema":   fmt.Sprintf("%d", SchemaVersion),
		"nexus:source-revision": sourceRevision,
		"nexus:workload":        "dappnode-nexus-gateway",
	}
}

func Encrypt(bundle SecretBundle, dataKey []byte, kmsKeyARN, region string) (Envelope, error) {
	if len(dataKey) != dataKeyBytes {
		return Envelope{}, fmt.Errorf("data key must be %d bytes", dataKeyBytes)
	}
	if err := ValidateSecretBundle(bundle, bundle.SourceRevision, time.Now().UTC()); err != nil {
		return Envelope{}, err
	}

	envelope := Envelope{
		SchemaVersion:     SchemaVersion,
		KMSKeyARN:         strings.TrimSpace(kmsKeyARN),
		AWSRegion:         strings.TrimSpace(region),
		EncryptionContext: EncryptionContext(bundle.SourceRevision),
	}
	if err := validateEnvelopeHeader(envelope, envelope.KMSKeyARN, envelope.AWSRegion, bundle.SourceRevision); err != nil {
		return Envelope{}, err
	}

	plaintext, err := json.Marshal(bundle)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal secret bundle: %w", err)
	}
	defer Clear(plaintext)
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return Envelope{}, fmt.Errorf("initialize bundle cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, fmt.Errorf("initialize bundle AEAD: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, fmt.Errorf("generate bundle nonce: %w", err)
	}
	aad, err := envelopeAAD(envelope)
	if err != nil {
		return Envelope{}, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)
	if len(ciphertext) > maxCiphertextSize {
		return Envelope{}, errors.New("encrypted secret bundle is too large")
	}
	envelope.Nonce = base64.StdEncoding.EncodeToString(nonce)
	envelope.Ciphertext = base64.StdEncoding.EncodeToString(ciphertext)
	return envelope, nil
}

func Decrypt(envelope Envelope, dataKey []byte, expectedKMSKeyARN, expectedRegion, expectedRevision string, now time.Time) (SecretBundle, error) {
	if len(dataKey) != dataKeyBytes {
		return SecretBundle{}, fmt.Errorf("data key must be %d bytes", dataKeyBytes)
	}
	if err := validateEnvelopeHeader(envelope, expectedKMSKeyARN, expectedRegion, expectedRevision); err != nil {
		return SecretBundle{}, err
	}
	nonce, err := decodeBounded("nonce", envelope.Nonce, 64)
	if err != nil {
		return SecretBundle{}, err
	}
	ciphertext, err := decodeBounded("ciphertext", envelope.Ciphertext, maxCiphertextSize)
	if err != nil {
		return SecretBundle{}, err
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return SecretBundle{}, fmt.Errorf("initialize bundle cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return SecretBundle{}, fmt.Errorf("initialize bundle AEAD: %w", err)
	}
	if len(nonce) != aead.NonceSize() {
		return SecretBundle{}, errors.New("secret bundle nonce has an invalid size")
	}
	aad, err := envelopeAAD(envelope)
	if err != nil {
		return SecretBundle{}, err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return SecretBundle{}, errors.New("authenticate and decrypt secret bundle")
	}
	defer Clear(plaintext)

	var bundle SecretBundle
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return SecretBundle{}, fmt.Errorf("decode secret bundle: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return SecretBundle{}, errors.New("secret bundle must contain exactly one JSON value")
	}
	if err := ValidateSecretBundle(bundle, expectedRevision, now); err != nil {
		return SecretBundle{}, err
	}
	return bundle, nil
}

func DecodeEncryptedDataKey(envelope Envelope) ([]byte, error) {
	return decodeBounded("encrypted_data_key", envelope.EncryptedDataKey, maxEncryptedKeySize)
}

func ValidateEnvelope(envelope Envelope, expectedKMSKeyARN, expectedRegion, expectedRevision string) error {
	if err := validateEnvelopeHeader(envelope, expectedKMSKeyARN, expectedRegion, expectedRevision); err != nil {
		return err
	}
	if _, err := DecodeEncryptedDataKey(envelope); err != nil {
		return err
	}
	if _, err := decodeBounded("nonce", envelope.Nonce, 64); err != nil {
		return err
	}
	if _, err := decodeBounded("ciphertext", envelope.Ciphertext, maxCiphertextSize); err != nil {
		return err
	}
	return nil
}

func ValidateSecretBundle(bundle SecretBundle, expectedRevision string, now time.Time) error {
	if bundle.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported secret bundle schema %d", bundle.SchemaVersion)
	}
	if bundle.SourceRevision == "" || bundle.SourceRevision != expectedRevision {
		return errors.New("secret bundle source revision does not match the measured build")
	}
	if bundle.IssuedAt.IsZero() {
		return errors.New("secret bundle issued_at is required")
	}
	if bundle.IssuedAt.After(now.Add(5 * time.Minute)) {
		return errors.New("secret bundle issued_at is in the future")
	}
	if bundle.ExpiresAt != nil {
		if !bundle.ExpiresAt.After(bundle.IssuedAt) {
			return errors.New("secret bundle expires_at must be after issued_at")
		}
		if !bundle.ExpiresAt.After(now) {
			return errors.New("secret bundle has expired")
		}
	}
	if len(bundle.Environment) == 0 {
		return errors.New("secret bundle environment is empty")
	}
	if err := validateEnvironment(bundle.Environment); err != nil {
		return err
	}
	if err := validateHTTPSURL("METERING_URL", bundle.Environment["METERING_URL"]); err != nil {
		return err
	}
	if strings.TrimSpace(bundle.Environment["METERING_TOKEN"]) == "" {
		return errors.New("METERING_TOKEN is required")
	}
	if routerURL := strings.TrimSpace(bundle.Environment["ROUTER_URL"]); routerURL != "" {
		if err := validateHTTPSURL("ROUTER_URL", routerURL); err != nil {
			return err
		}
	}
	return nil
}

func validateEnvironment(environment map[string]string) error {
	total := 0
	for key, value := range environment {
		if _, ok := allowedEnvironmentKeys[key]; !ok && !providerSecretPattern.MatchString(key) {
			return fmt.Errorf("environment key %q is not allowed in an enclave secret bundle", key)
		}
		if value == "" {
			return fmt.Errorf("environment value %q is empty", key)
		}
		if len(value) > maxEnvironmentValue || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("environment value %q has an invalid size or format", key)
		}
		total += len(key) + len(value)
		if total > maxEnvironmentBytes {
			return errors.New("secret bundle environment is too large")
		}
	}
	return nil
}

func validateHTTPSURL(name, value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an HTTPS origin without credentials or fragments", name)
	}
	return nil
}

func validateEnvelopeHeader(envelope Envelope, expectedKMSKeyARN, expectedRegion, expectedRevision string) error {
	if envelope.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported encrypted envelope schema %d", envelope.SchemaVersion)
	}
	if envelope.KMSKeyARN == "" || envelope.KMSKeyARN != expectedKMSKeyARN {
		return errors.New("encrypted envelope KMS key does not match the measured build")
	}
	if envelope.AWSRegion == "" || envelope.AWSRegion != expectedRegion {
		return errors.New("encrypted envelope AWS region does not match the measured build")
	}
	expectedContext := EncryptionContext(expectedRevision)
	if len(envelope.EncryptionContext) != len(expectedContext) {
		return errors.New("encrypted envelope KMS context is invalid")
	}
	for key, value := range expectedContext {
		if envelope.EncryptionContext[key] != value {
			return errors.New("encrypted envelope KMS context is invalid")
		}
	}
	return nil
}

func envelopeAAD(envelope Envelope) ([]byte, error) {
	header := struct {
		SchemaVersion     int               `json:"schema_version"`
		KMSKeyARN         string            `json:"kms_key_arn"`
		AWSRegion         string            `json:"aws_region"`
		EncryptionContext map[string]string `json:"encryption_context"`
	}{
		SchemaVersion:     envelope.SchemaVersion,
		KMSKeyARN:         envelope.KMSKeyARN,
		AWSRegion:         envelope.AWSRegion,
		EncryptionContext: envelope.EncryptionContext,
	}
	aad, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("encode encrypted envelope header: %w", err)
	}
	return aad, nil
}

func decodeBounded(name, encoded string, maximum int) ([]byte, error) {
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(maximum) {
		return nil, fmt.Errorf("%s has an invalid size", name)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > maximum {
		return nil, fmt.Errorf("%s is not valid bounded base64", name)
	}
	return decoded, nil
}

func Clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
