package services

import (
	"bytes"
	"encoding/json"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// DefaultMaxOutputTokens is the output limit of a chat request that sets
// none. It equals the output Metering holds for such a request
// (metering.DefaultOutputHoldTokens in the Nexus repo), so the answer can
// never outrun its hold.
const DefaultMaxOutputTokens = 32_768

// DefaultGuestMaxOutputTokens is the output limit of a guest's chat request
// unless GUEST_MAX_OUTPUT_TOKENS sets another. It keeps a guest's hold well
// inside its small credit and still covers almost every answer, reasoning
// included: over a week of production traffic on DeepSeek V4.1 Flash, answers
// averaged about 1,100 output tokens and the longest was about 15,000
// (2026-11-04).
const DefaultGuestMaxOutputTokens = 8192

// capOutput writes an output limit into a chat request: the client's own
// limit when it is smaller, otherwise limit. Metering holds credit for that
// limit and the provider enforces it, so an answer never costs more than its
// hold and no request can run up a debt by leaving the limit unset. The
// provider adapter still clamps it to the model's maximum.
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

// outputLimit is the output limit of a chat request from this account.
func (s *GenerateService) outputLimit(account domain.Account) int {
	if account.IsGuest {
		return s.guestMaxOutputTokens
	}
	return DefaultMaxOutputTokens
}
