package domain

import (
	"context"
	"fmt"
)

// ErrCodeAttestedInferenceUnavailable refuses a request that asked for
// attested inference when its model can't be served that way.
const ErrCodeAttestedInferenceUnavailable = "attested_inference_unavailable"

type attestedInferenceKey struct{}

// WithAttestedInference marks a request whose prompt may be read only by
// attested code: the Gateway enclave itself and a provider whose hardware
// attestation the Gateway verifies before sending it anything. Such a request
// is never routed by the Router, never sent to the PII analyzer, and never
// sent to a provider without that verification, primary or fallback.
//
// Only the confidential endpoint sets it, from a field inside the decrypted
// request, so nothing outside the enclave can add or remove it.
func WithAttestedInference(ctx context.Context) context.Context {
	return context.WithValue(ctx, attestedInferenceKey{}, true)
}

// AttestedInferenceRequired reports whether WithAttestedInference marked ctx.
func AttestedInferenceRequired(ctx context.Context) bool {
	required, _ := ctx.Value(attestedInferenceKey{}).(bool)
	return required
}

// ErrAttestedInferenceUnavailable is a request that asked for attested
// inference on a model or route that can't provide it. Nothing was sent.
func ErrAttestedInferenceUnavailable(model string) *GatewayError {
	return &GatewayError{HTTPStatus: 422, Type: ErrTypeInvalidRequest, Code: ErrCodeAttestedInferenceUnavailable,
		Message: fmt.Sprintf("Model '%s' can't be served so that only attested code reads the prompt. Nothing was sent.", model)}
}
