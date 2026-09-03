package ports

import "context"

// Attester returns a hardware-signed attestation document containing nonce,
// userData, and publicKey. Implementations must never synthesize software
// proofs.
type Attester interface {
	Attest(ctx context.Context, nonce, userData, publicKey []byte) ([]byte, error)
}
