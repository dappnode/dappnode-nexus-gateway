package services

import (
	"bytes"
	"encoding/json"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// DefaultGuestMaxOutputTokens is the output limit of a guest's chat request
// unless GUEST_MAX_OUTPUT_TOKENS sets another. Without it a guest's request
// would hold the model's maximum output, far more than a guest's credit. It
// still covers almost every answer, reasoning included: over a week of production traffic on DeepSeek V4.1 Flash, answers
// averaged about 1,100 output tokens and the longest was about 15,000
// (2026-11-04).
const DefaultGuestMaxOutputTokens = 8192

// capOutput writes an output limit into a guest's chat request: the guest's
// own limit when it is smaller, otherwise limit. Metering holds credit for
// that limit and the provider enforces it. Other accounts' requests are left
// as sent: without max_tokens, Metering holds the model's maximum output.
func capOutput(raw []byte, req domain.GenerateRequest, limit int) ([]byte, domain.GenerateRequest, error) {
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 && *req.MaxOutputTokens <= limit {
		return raw, req, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // Forward numbers exactly as sent.
	var body map[string]any
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, req, domain.ErrInvalidField("invalid JSON body")
	}
	// Keep the client's field name, or use OpenAI's current one. The provider
	// adapter sends max_tokens to providers that ignore max_completion_tokens
	// (DeepSeek); every other provider enforces both (checked 2026-11-04).
	// The mapper refuses requests that set both.
	if _, ok := body["max_tokens"]; ok {
		body["max_tokens"] = limit
	} else {
		body["max_completion_tokens"] = limit
	}
	rewritten, err := json.Marshal(body)
	if err != nil {
		return nil, req, domain.ErrInternal("an internal error occurred").WithMeta("reason", "re-encode output limit", "error", err.Error())
	}
	req.MaxOutputTokens = &limit
	return rewritten, req, nil
}
