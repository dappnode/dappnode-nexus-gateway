package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/dappnode/dappnode-nexus-gateway/internal/enclavebundle"
)

const maxInputBytes = 128 * 1024

type secretInput struct {
	ExpiresAt   *time.Time        `json:"expires_at,omitempty"`
	Environment map[string]string `json:"environment"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "encrypt" {
		fmt.Fprintln(os.Stderr, "usage: nexus-enclave-config encrypt [flags]")
		os.Exit(2)
	}
	if err := runEncrypt(context.Background(), os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "nexus-enclave-config:", err)
		os.Exit(1)
	}
}

func runEncrypt(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("encrypt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("in", "", "0600 JSON secret input path, or - for stdin")
	outputPath := flags.String("out", "", "encrypted envelope output path, or - for stdout")
	region := flags.String("region", "", "AWS region containing the KMS key")
	keyARN := flags.String("kms-key-arn", "", "customer-managed symmetric KMS key ARN")
	sourceRevision := flags.String("source-revision", "", "full measured gateway Git revision")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *inputPath == "" || *outputPath == "" || *region == "" || *keyARN == "" || *sourceRevision == "" {
		return errors.New("--in, --out, --region, --kms-key-arn, and --source-revision are required")
	}

	inputBytes, err := readSecretInput(*inputPath)
	if err != nil {
		return err
	}
	defer enclavebundle.Clear(inputBytes)
	var input secretInput
	if err := decodeStrictJSON(inputBytes, &input); err != nil {
		return fmt.Errorf("decode secret input: %w", err)
	}

	now := time.Now().UTC()
	bundle := enclavebundle.SecretBundle{
		SchemaVersion:  enclavebundle.SchemaVersion,
		SourceRevision: strings.TrimSpace(*sourceRevision),
		IssuedAt:       now,
		ExpiresAt:      input.ExpiresAt,
		Environment:    input.Environment,
	}
	if err := enclavebundle.ValidateSecretBundle(bundle, bundle.SourceRevision, now); err != nil {
		return fmt.Errorf("validate secret input: %w", err)
	}

	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return fmt.Errorf("generate data key: %w", err)
	}
	defer enclavebundle.Clear(dataKey)

	envelope, err := enclavebundle.Encrypt(bundle, dataKey, strings.TrimSpace(*keyARN), strings.TrimSpace(*region))
	if err != nil {
		return err
	}
	awsConfig, err := config.LoadDefaultConfig(ctx, config.WithRegion(envelope.AWSRegion))
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	result, err := kms.NewFromConfig(awsConfig).Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(envelope.KMSKeyARN),
		Plaintext:         dataKey,
		EncryptionContext: envelope.EncryptionContext,
	})
	if err != nil {
		return fmt.Errorf("wrap data key with AWS KMS: %w", err)
	}
	if len(result.CiphertextBlob) == 0 {
		return errors.New("AWS KMS returned an empty encrypted data key")
	}
	if result.KeyId == nil || *result.KeyId != envelope.KMSKeyARN {
		if result.KeyId == nil {
			return errors.New("AWS KMS returned no key ID")
		}
		return fmt.Errorf("AWS KMS used unexpected key %q", *result.KeyId)
	}
	envelope.EncryptedDataKey = base64.StdEncoding.EncodeToString(result.CiphertextBlob)

	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("encode encrypted envelope: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeEnvelope(*outputPath, encoded)
}

func readSecretInput(path string) ([]byte, error) {
	var reader io.Reader
	if path == "-" {
		reader = os.Stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open secret input: %w", err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect secret input: %w", err)
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("secret input file must not be readable or writable by group/other (use chmod 600)")
		}
		reader = file
	}
	payload, err := io.ReadAll(io.LimitReader(reader, maxInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read secret input: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxInputBytes {
		return nil, errors.New("secret input has an invalid size")
	}
	return payload, nil
}

func writeEnvelope(path string, payload []byte) error {
	if path == "-" {
		_, err := os.Stdout.Write(payload)
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create encrypted envelope without overwriting: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("write encrypted envelope: %w", err)
	}
	return nil
}

func decodeStrictJSON(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
