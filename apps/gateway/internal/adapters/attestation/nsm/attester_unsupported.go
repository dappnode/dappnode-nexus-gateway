//go:build !linux

package nsmattestation

import (
	"context"
	"errors"
)

type Attester struct{}

func New() (*Attester, error) {
	return nil, errors.New("AWS Nitro attestation requires Linux")
}

func (*Attester) Attest(context.Context, []byte, []byte, []byte) ([]byte, error) {
	return nil, errors.New("AWS Nitro attestation requires Linux")
}

func (*Attester) Close() error {
	return nil
}
