//go:build linux

package nsmattestation

import (
	"context"
	"errors"
	"fmt"

	gonitro "github.com/0xsequence/nsm"
	"github.com/0xsequence/nsm/request"
	"github.com/0xsequence/nsm/response"
)

type session interface {
	Send(request.Request) (response.Response, error)
	Close() error
}

// Attester obtains documents directly from the Nitro Secure Module.
type Attester struct {
	session session
}

func New() (*Attester, error) {
	session, err := gonitro.OpenDefaultSession()
	if err != nil {
		return nil, fmt.Errorf("open Nitro Secure Module: %w", err)
	}
	return &Attester{session: session}, nil
}

func (a *Attester) Attest(ctx context.Context, nonce, userData, publicKey []byte) ([]byte, error) {
	if a == nil || a.session == nil {
		return nil, errors.New("Nitro Secure Module session is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result, err := a.session.Send(&request.Attestation{
		Nonce:     append([]byte(nil), nonce...),
		UserData:  append([]byte(nil), userData...),
		PublicKey: append([]byte(nil), publicKey...),
	})
	if err != nil {
		return nil, fmt.Errorf("request Nitro attestation: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("Nitro attestation rejected: %s", result.Error)
	}
	if result.Attestation == nil || len(result.Attestation.Document) == 0 {
		return nil, errors.New("Nitro Secure Module returned no attestation document")
	}
	return append([]byte(nil), result.Attestation.Document...), nil
}

func (a *Attester) Close() error {
	if a == nil || a.session == nil {
		return nil
	}
	return a.session.Close()
}
