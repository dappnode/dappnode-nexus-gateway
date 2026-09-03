//go:build nitro_enclave

package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/0xsequence/nsm"
	"github.com/0xsequence/nsm/request"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/enclave/profile"
	"github.com/dappnode/dappnode-nexus-gateway/internal/enclavebundle"
	"github.com/mdlayher/vsock"
)

func decryptDataKey(ctx context.Context, credentialsValue enclavebundle.AWSCredentials, envelope enclavebundle.Envelope) ([]byte, error) {
	encryptedDataKey, err := enclavebundle.DecodeEncryptedDataKey(envelope)
	if err != nil {
		return nil, err
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral KMS recipient key: %w", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal ephemeral KMS recipient key: %w", err)
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate KMS attestation nonce: %w", err)
	}

	session, err := nsm.OpenDefaultSession()
	if err != nil {
		return nil, fmt.Errorf("open Nitro Secure Module for KMS: %w", err)
	}
	defer session.Close()
	attestation, err := session.Send(&request.Attestation{
		Nonce:     nonce,
		UserData:  profile.ManifestDigest(),
		PublicKey: publicKeyDER,
	})
	if err != nil {
		return nil, fmt.Errorf("request KMS recipient attestation: %w", err)
	}
	if attestation.Error != "" {
		return nil, fmt.Errorf("Nitro Secure Module rejected KMS attestation: %s", attestation.Error)
	}
	if attestation.Attestation == nil || len(attestation.Attestation.Document) == 0 {
		return nil, errors.New("Nitro Secure Module returned no KMS attestation document")
	}

	provider := credentials.NewStaticCredentialsProvider(
		credentialsValue.AccessKeyID,
		credentialsValue.SecretAccessKey,
		credentialsValue.SessionToken,
	)
	awsConfig, err := config.LoadDefaultConfig(
		ctx,
		config.WithRegion(profile.AWSRegion),
		config.WithCredentialsProvider(provider),
		config.WithHTTPClient(kmsHTTPClient(profile.AWSRegion, profile.KMSKeyARN)),
	)
	if err != nil {
		return nil, fmt.Errorf("load enclave AWS KMS configuration: %w", err)
	}
	result, err := kms.NewFromConfig(awsConfig).Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    encryptedDataKey,
		EncryptionContext: envelope.EncryptionContext,
		KeyId:             aws.String(profile.KMSKeyARN),
		Recipient: &types.RecipientInfo{
			AttestationDocument:    attestation.Attestation.Document,
			KeyEncryptionAlgorithm: types.KeyEncryptionMechanismRsaesOaepSha256,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("AWS KMS attested decrypt: %w", err)
	}
	if len(result.Plaintext) != 0 {
		enclavebundle.Clear(result.Plaintext)
		return nil, errors.New("AWS KMS returned plaintext instead of CiphertextForRecipient")
	}
	if len(result.CiphertextForRecipient) == 0 {
		return nil, errors.New("AWS KMS returned no CiphertextForRecipient")
	}
	if result.KeyId == nil || *result.KeyId != profile.KMSKeyARN {
		return nil, errors.New("AWS KMS decrypted with an unexpected key")
	}

	dataKey, err := decryptKMSRecipient(privateKey, result.CiphertextForRecipient)
	if err != nil {
		return nil, fmt.Errorf("decrypt KMS recipient response: %w", err)
	}
	if len(dataKey) != 32 {
		enclavebundle.Clear(dataKey)
		return nil, errors.New("AWS KMS released an invalid data key size")
	}
	return dataKey, nil
}

func kmsHTTPClient(region, keyARN string) *http.Client {
	host := "kms." + region + ".amazonaws.com"
	parts := strings.Split(keyARN, ":")
	if len(parts) > 1 && parts[1] == "aws-cn" {
		host += ".cn"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: host,
	}
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return vsock.Dial(profile.ParentCID, profile.KMSVsockPort, nil)
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}
