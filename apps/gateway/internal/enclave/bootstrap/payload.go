package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/internal/enclavebundle"
)

type dataKeyDecryptor func(context.Context, enclavebundle.AWSCredentials, enclavebundle.Envelope) ([]byte, error)
type environmentSetter func(string, string) error

func loadPayload(
	ctx context.Context,
	payload []byte,
	now time.Time,
	expectedKMSKeyARN string,
	expectedRegion string,
	expectedRevision string,
	decrypt dataKeyDecryptor,
	setenv environmentSetter,
) error {
	if decrypt == nil || setenv == nil {
		return errors.New("enclave bootstrap dependencies are unavailable")
	}
	var request enclavebundle.BootstrapRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("decode enclave bootstrap: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("enclave bootstrap must contain exactly one JSON value")
	}
	if err := enclavebundle.ValidateBootstrapRequest(request, now); err != nil {
		return err
	}
	if err := enclavebundle.ValidateEnvelope(request.Envelope, expectedKMSKeyARN, expectedRegion, expectedRevision); err != nil {
		return err
	}

	dataKey, err := decrypt(ctx, request.Credentials, request.Envelope)
	if err != nil {
		return fmt.Errorf("release enclave data key: %w", err)
	}
	defer enclavebundle.Clear(dataKey)
	bundle, err := enclavebundle.Decrypt(
		request.Envelope,
		dataKey,
		expectedKMSKeyARN,
		expectedRegion,
		expectedRevision,
		now,
	)
	if err != nil {
		return err
	}

	for key, value := range bundle.Environment {
		if err := setenv(key, value); err != nil {
			return fmt.Errorf("apply enclave configuration %q: %w", key, err)
		}
	}
	return nil
}

func clear(value []byte) {
	enclavebundle.Clear(value)
}
