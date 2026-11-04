package services

import (
	"bytes"
	"encoding/json"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// DefaultGuestMaxOutputTokens is the output limit of a guest's chat request
// unless GUEST_MAX_OUTPUT_TOKENS sets another. It covers almost every answer,
// reasoning included: over a week of production traffic on DeepSeek V4.1
// Flash, answers averaged about 1,100 output tokens and the longest was about
// 15,000 (2026-11-04).
const DefaultGuestMaxOutputTokens = 8192

// capGuestOutput writes an output limit into a guest's chat request. A guest
// is an ordinary account with a small credit and no way to pay a debt, so an
// answer must never cost more than its hold. Metering holds credit for the
// request's output limit, and the provider enforces the limit written here;
// without one, the request would go upstream unbounded and could overrun the
// hold (docs/METERING_HOLDS.md in the Nexus repo). A smaller limit the guest
// set is kept. Everything else about a guest's request is untouched.
func capGuestOutput(raw []byte, req domain.GenerateRequest, limit int) ([]byte, domain.GenerateRequest, error) {
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 && *req.MaxOutputTokens < limit {
		limit = *req.MaxOutputTokens
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // Forward numbers exactly as sent.
	var body map[string]any
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, req, domain.ErrInvalidField("invalid JSON body")
	}
	// Keep the client's field name; providers that need max_tokens get it from
	// the provider adapter. The mapper refuses requests that set both.
	if _, ok := body["max_completion_tokens"]; ok {
		body["max_completion_tokens"] = limit
	} else {
		body["max_tokens"] = limit
	}
	rewritten, err := json.Marshal(body)
	if err != nil {
		return nil, req, domain.ErrInternal("an internal error occurred").WithMeta("reason", "re-encode guest request", "error", err.Error())
	}
	req.MaxOutputTokens = &limit
	return rewritten, req, nil
}
