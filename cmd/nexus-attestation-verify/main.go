package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/anchorageoss/awsnitroverifier"
	"github.com/tinfoilsh/encrypted-http-body-protocol/identity"
)

const (
	nonceSize                  = 32
	pcrSize                    = 48
	maxResponseSize            = 1 << 20
	expectedAWSRootFingerprint = "641a0321a3e244efe456463195d606317ed7cdcc3c1756e09893f3c68f79bb5b"
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type attestationVerifier interface {
	Validate([]byte) (*awsnitroverifier.ValidationResult, error)
}

type endpointResponse struct {
	Document         string          `json:"document"`
	Format           string          `json:"format"`
	Encoding         string          `json:"encoding"`
	UserData         string          `json:"user_data"`
	UserDataEncoding string          `json:"user_data_encoding"`
	HPKEPublicKey    string          `json:"hpke_public_key"`
	HPKEKeyEncoding  string          `json:"hpke_public_key_encoding"`
	Manifest         json.RawMessage `json:"manifest"`
}

type fetchedAttestation struct {
	document      []byte
	manifest      json.RawMessage
	userData      []byte
	hpkePublicKey []byte
}

type verificationSummary struct {
	Verified              bool              `json:"verified"`
	AttestedAt            string            `json:"attested_at"`
	AttestationTimestamp  uint64            `json:"attestation_timestamp_ms"`
	RootFingerprintSHA256 string            `json:"root_fingerprint_sha256"`
	ManifestSHA384        string            `json:"manifest_sha384"`
	HPKEPublicKey         string            `json:"hpke_public_key"`
	PCRs                  map[string]string `json:"pcrs_sha384"`
	Manifest              json.RawMessage   `json:"manifest"`
}

type cliConfig struct {
	endpoint      string
	pcrs          map[uint][]byte
	timeout       time.Duration
	maxAge        time.Duration
	maxFutureSkew time.Duration
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	config, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		fmt.Fprintf(stderr, "error: generate nonce: %v\n", err)
		return 1
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	client := &http.Client{
		Transport: transport,
		Timeout:   config.timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("redirects are not allowed")
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), config.timeout)
	defer cancel()

	fetched, err := fetchAttestation(ctx, client, config.endpoint, nonce)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	rules := make([]awsnitroverifier.PCRRule, 0, 3)
	for _, index := range []uint{0, 1, 2} {
		rules = append(rules, awsnitroverifier.PCRRule{Index: index, Value: config.pcrs[index]})
	}
	verifier := awsnitroverifier.NewVerifier(awsnitroverifier.AWSNitroVerifierOptions{
		SkipTimestampCheck: false,
		PCRRules:           rules,
	})

	summary, err := validateAttestation(fetched, nonce, config.pcrs, time.Now(), config.maxAge, config.maxFutureSkew, verifier)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(summary); err != nil {
		fmt.Fprintf(stderr, "error: write result: %v\n", err)
		return 1
	}
	return 0
}

func parseFlags(args []string, stderr io.Writer) (*cliConfig, error) {
	flags := flag.NewFlagSet("nexus-attestation-verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: nexus-attestation-verify --url URL --pcr0 HEX --pcr1 HEX --pcr2 HEX [options]\n")
		flags.PrintDefaults()
	}

	endpoint := flags.String("url", "", "full HTTPS URL of the gateway /v1/attestation endpoint")
	pcr0 := flags.String("pcr0", "", "expected PCR0 as 96 hexadecimal characters")
	pcr1 := flags.String("pcr1", "", "expected PCR1 as 96 hexadecimal characters")
	pcr2 := flags.String("pcr2", "", "expected PCR2 as 96 hexadecimal characters")
	timeout := flags.Duration("timeout", 15*time.Second, "HTTP request timeout")
	maxAge := flags.Duration("max-age", 2*time.Minute, "maximum accepted attestation age")
	maxFutureSkew := flags.Duration("max-future-skew", 30*time.Second, "maximum accepted clock skew into the future")

	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, fmt.Errorf("unexpected positional arguments")
	}
	if err := validateEndpointURL(*endpoint); err != nil {
		return nil, err
	}
	if *timeout <= 0 {
		return nil, fmt.Errorf("--timeout must be positive")
	}
	if *maxAge <= 0 {
		return nil, fmt.Errorf("--max-age must be positive")
	}
	if *maxFutureSkew < 0 {
		return nil, fmt.Errorf("--max-future-skew must not be negative")
	}

	pcrs := make(map[uint][]byte, 3)
	for _, item := range []struct {
		index uint
		value string
	}{
		{index: 0, value: *pcr0},
		{index: 1, value: *pcr1},
		{index: 2, value: *pcr2},
	} {
		decoded, err := parsePCR(item.index, item.value)
		if err != nil {
			return nil, err
		}
		pcrs[item.index] = decoded
	}

	return &cliConfig{
		endpoint:      *endpoint,
		pcrs:          pcrs,
		timeout:       *timeout,
		maxAge:        *maxAge,
		maxFutureSkew: *maxFutureSkew,
	}, nil
}

func validateEndpointURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("--url is required")
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("invalid --url: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return fmt.Errorf("--url must be an absolute HTTPS URL")
	}
	if parsed.User != nil {
		return fmt.Errorf("--url must not contain user information")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("--url must not contain a fragment")
	}
	return nil
}

func parsePCR(index uint, encoded string) ([]byte, error) {
	if len(encoded) != pcrSize*2 {
		return nil, fmt.Errorf("--pcr%d must contain exactly %d hexadecimal characters", index, pcrSize*2)
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid --pcr%d: %w", index, err)
	}
	if allZero(decoded) {
		return nil, fmt.Errorf("--pcr%d is all zero, which identifies an unsafe debug measurement", index)
	}
	return decoded, nil
}

func fetchAttestation(ctx context.Context, client httpDoer, endpoint string, nonce []byte) (*fetchedAttestation, error) {
	if len(nonce) != nonceSize {
		return nil, fmt.Errorf("nonce must be exactly %d bytes", nonceSize)
	}

	body, err := json.Marshal(struct {
		Nonce string `json:"nonce"`
	}{Nonce: base64.RawURLEncoding.EncodeToString(nonce)})
	if err != nil {
		return nil, fmt.Errorf("encode attestation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create attestation request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "nexus-attestation-verify/1")

	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request attestation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("attestation endpoint returned HTTP %d", response.StatusCode)
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return nil, fmt.Errorf("attestation endpoint returned a non-JSON content type")
	}

	limited := io.LimitReader(response.Body, maxResponseSize+1)
	responseBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read attestation response: %w", err)
	}
	if len(responseBytes) > maxResponseSize {
		return nil, fmt.Errorf("attestation response exceeds %d bytes", maxResponseSize)
	}

	var payload endpointResponse
	decoder := json.NewDecoder(bytes.NewReader(responseBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode attestation response: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode attestation response: %w", err)
	}
	if payload.Format != "aws-nitro-cose-sign1" || payload.Encoding != "base64" {
		return nil, fmt.Errorf("unsupported attestation format or encoding")
	}
	if payload.UserDataEncoding != "sha384-hex" {
		return nil, fmt.Errorf("unsupported attestation user_data encoding")
	}
	if payload.HPKEKeyEncoding != "raw-x25519-hex" {
		return nil, fmt.Errorf("unsupported attestation HPKE public key encoding")
	}
	if len(payload.Manifest) == 0 || !json.Valid(payload.Manifest) || bytes.Equal(payload.Manifest, []byte("null")) {
		return nil, fmt.Errorf("attestation response contains an invalid manifest")
	}

	document, err := strictBase64(payload.Document)
	if err != nil || len(document) == 0 {
		return nil, fmt.Errorf("attestation response contains an invalid document")
	}
	userData, err := hex.DecodeString(payload.UserData)
	if err != nil || len(userData) != sha512.Size384 {
		return nil, fmt.Errorf("attestation response contains invalid user_data")
	}
	hpkePublicKey, err := hex.DecodeString(payload.HPKEPublicKey)
	if err != nil || len(hpkePublicKey) != 32 || hex.EncodeToString(hpkePublicKey) != payload.HPKEPublicKey || allZero(hpkePublicKey) {
		return nil, fmt.Errorf("attestation response contains an invalid HPKE public key")
	}
	if _, err := identity.FromPublicKeyBytes(hpkePublicKey); err != nil {
		return nil, fmt.Errorf("attestation response contains an invalid HPKE public key")
	}

	return &fetchedAttestation{
		document:      document,
		manifest:      append(json.RawMessage(nil), payload.Manifest...),
		userData:      append([]byte(nil), userData...),
		hpkePublicKey: append([]byte(nil), hpkePublicKey...),
	}, nil
}

func validateAttestation(
	fetched *fetchedAttestation,
	nonce []byte,
	expectedPCRs map[uint][]byte,
	now time.Time,
	maxAge time.Duration,
	maxFutureSkew time.Duration,
	verifier attestationVerifier,
) (*verificationSummary, error) {
	if fetched == nil {
		return nil, errors.New("attestation response is nil")
	}
	if len(nonce) != nonceSize {
		return nil, fmt.Errorf("nonce must be exactly %d bytes", nonceSize)
	}
	if now.IsZero() || maxAge <= 0 || maxFutureSkew < 0 {
		return nil, errors.New("invalid verifier time policy")
	}

	manifestDigest := sha512.Sum384(fetched.manifest)
	if subtle.ConstantTimeCompare(fetched.userData, manifestDigest[:]) != 1 {
		return nil, errors.New("response user_data does not match SHA-384(manifest)")
	}

	result, err := verifier.Validate(fetched.document)
	if err != nil {
		return nil, fmt.Errorf("parse attestation document: %w", err)
	}
	if result == nil {
		return nil, errors.New("attestation verifier returned no result")
	}
	if !result.Valid {
		return nil, fmt.Errorf("AWS Nitro attestation validation failed: %s", validationErrors(result.Errors))
	}
	if !result.ChainTrusted || result.RootFingerprint != expectedAWSRootFingerprint {
		return nil, errors.New("attestation certificate chain is not rooted in the expected AWS Nitro CA")
	}
	if result.Document == nil {
		return nil, errors.New("attestation document is missing decoded fields")
	}
	if len(result.Nonce) != nonceSize || subtle.ConstantTimeCompare(result.Nonce, nonce) != 1 {
		return nil, errors.New("attestation nonce does not match the request")
	}
	if len(result.UserData) != sha512.Size384 || subtle.ConstantTimeCompare(result.UserData, manifestDigest[:]) != 1 {
		return nil, errors.New("signed user_data does not match SHA-384(manifest)")
	}
	if len(fetched.hpkePublicKey) != 32 || allZero(fetched.hpkePublicKey) {
		return nil, errors.New("response HPKE public key is missing or malformed")
	}
	if len(result.PublicKey) != 32 || allZero(result.PublicKey) || subtle.ConstantTimeCompare(result.PublicKey, fetched.hpkePublicKey) != 1 {
		return nil, errors.New("signed HPKE public key does not match the response")
	}
	if len(result.Document.PublicKey) != 32 || subtle.ConstantTimeCompare(result.Document.PublicKey, fetched.hpkePublicKey) != 1 {
		return nil, errors.New("attestation document HPKE public key does not match the response")
	}
	if result.Document.Timestamp > math.MaxInt64 {
		return nil, errors.New("attestation timestamp is out of range")
	}
	attestedAt := time.UnixMilli(int64(result.Document.Timestamp))
	if attestedAt.After(now.Add(maxFutureSkew)) {
		return nil, errors.New("attestation timestamp is too far in the future")
	}
	if attestedAt.Before(now.Add(-maxAge)) {
		return nil, errors.New("attestation timestamp is stale")
	}

	pcrSummary := make(map[string]string, 3)
	for _, index := range []uint{0, 1, 2} {
		expected, ok := expectedPCRs[index]
		if !ok || len(expected) != pcrSize || allZero(expected) {
			return nil, fmt.Errorf("expected PCR%d is missing or unsafe", index)
		}
		actual, ok := result.Document.PCRs[index]
		if !ok || len(actual) != pcrSize {
			return nil, fmt.Errorf("attestation PCR%d is missing or malformed", index)
		}
		if allZero(actual) {
			return nil, fmt.Errorf("attestation PCR%d is all zero (debug enclave)", index)
		}
		if subtle.ConstantTimeCompare(actual, expected) != 1 {
			return nil, fmt.Errorf("attestation PCR%d does not match the expected measurement", index)
		}
		pcrSummary[fmt.Sprintf("%d", index)] = hex.EncodeToString(actual)
	}

	return &verificationSummary{
		Verified:              true,
		AttestedAt:            attestedAt.UTC().Format(time.RFC3339Nano),
		AttestationTimestamp:  result.Document.Timestamp,
		RootFingerprintSHA256: result.RootFingerprint,
		ManifestSHA384:        hex.EncodeToString(manifestDigest[:]),
		HPKEPublicKey:         hex.EncodeToString(fetched.hpkePublicKey),
		PCRs:                  pcrSummary,
		Manifest:              append(json.RawMessage(nil), fetched.manifest...),
	}, nil
}

func strictBase64(encoded string) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("non-canonical base64")
	}
	return decoded, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
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

func validationErrors(errs []error) string {
	if len(errs) == 0 {
		return "unspecified validation failure"
	}
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			messages = append(messages, err.Error())
		}
	}
	if len(messages) == 0 {
		return "unspecified validation failure"
	}
	return strings.Join(messages, "; ")
}

func allZero(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	var aggregate byte
	for _, current := range value {
		aggregate |= current
	}
	return aggregate == 0
}
