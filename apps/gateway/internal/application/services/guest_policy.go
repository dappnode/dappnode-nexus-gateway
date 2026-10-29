package services

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// GuestPolicy narrows what a guest chat key can do. A guest is a landing-page
// visitor without a login, holding a key funded with a fraction of a cent
// (see the Nexus repo's docs/guest-chat.md).
//
// Metering refuses any request whose worst-case cost the guest's credit
// cannot cover. This policy keeps that worst case small and honest:
//
//   - the model is one of Models, never a router or a pricier model;
//   - the request is at most MaxRequestBytes, so the prompt is at most that
//     many tokens: tokenizers emit at most one token per byte of text, and the
//     JSON around the text is longer than the chat template that replaces it;
//   - the output is at most MaxOutputTokens, written into the request so the
//     provider enforces it too;
//   - only plain chat fields and text content are accepted, so no provider
//     option (reasoning budgets, tools, images) can raise the cost.
type GuestPolicy struct {
	Models          map[string]bool
	MaxRequestBytes int
	MaxOutputTokens int
}

// guestPromptOverheadTokens covers what reaches the model beyond the bytes
// the gateway reserved for: the chat template's role markers and the fields
// the provider adapter adds (model name, stream options).
const guestPromptOverheadTokens = 256

// guestRequestFields are the top-level request fields a guest may send.
var guestRequestFields = map[string]bool{
	"model": true, "messages": true, "stream": true, "stream_options": true,
	"max_tokens": true, "max_completion_tokens": true,
	"temperature": true, "top_p": true, "presence_penalty": true, "frequency_penalty": true,
	"stop": true, "seed": true, "user": true,
}

// guestMessageFields are the fields a guest message may carry.
var guestMessageFields = map[string]bool{"role": true, "content": true, "name": true}

// guestRoles are the message roles a guest may send.
var guestRoles = map[string]bool{"system": true, "user": true, "assistant": true}

// Apply checks a guest's request and returns the body to forward, with its
// output limit set, and the request summary updated to match.
func (p GuestPolicy) Apply(endpoint string, raw []byte, req domain.GenerateRequest) ([]byte, domain.GenerateRequest, error) {
	if endpoint != domain.EndpointChatCompletions {
		return nil, req, domain.ErrGuestNotAllowed("this endpoint")
	}
	if len(raw) > p.MaxRequestBytes {
		return nil, req, domain.ErrGuestRequestTooLarge(p.MaxRequestBytes)
	}
	if !p.Models[req.PublicModelID] {
		return nil, req, domain.ErrGuestNotAllowed(fmt.Sprintf("the model '%s'", req.PublicModelID))
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // Forward numbers exactly as sent.
	var body map[string]any
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, req, domain.ErrInvalidField("invalid JSON body")
	}
	for field := range body {
		if !guestRequestFields[field] {
			return nil, req, domain.ErrGuestNotAllowed(fmt.Sprintf("the field '%s'", field))
		}
	}
	messages, ok := body["messages"].([]any)
	if !ok {
		return nil, req, domain.ErrInvalidField("messages must be an array")
	}
	for i, message := range messages {
		if err := checkGuestMessage(i, message); err != nil {
			return nil, req, err
		}
	}

	limit := p.MaxOutputTokens
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 && *req.MaxOutputTokens < limit {
		limit = *req.MaxOutputTokens
	}
	// Keep the client's field name; providers that need max_tokens get it
	// from the provider adapter.
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

// PromptTokenBound is the most prompt tokens a guest request can reach the
// model with, given the exact body sent upstream (after PII masking).
func (p GuestPolicy) PromptTokenBound(upstreamBody []byte) int {
	return len(upstreamBody) + guestPromptOverheadTokens
}

// checkGuestMessage accepts a system, user or assistant message whose content
// is text: a string, or text parts.
func checkGuestMessage(i int, message any) error {
	fields, ok := message.(map[string]any)
	if !ok {
		return domain.ErrInvalidField(fmt.Sprintf("message[%d] must be an object", i))
	}
	for field := range fields {
		if !guestMessageFields[field] {
			return domain.ErrGuestNotAllowed(fmt.Sprintf("the message field '%s'", field))
		}
	}
	if role, _ := fields["role"].(string); !guestRoles[role] {
		return domain.ErrGuestNotAllowed(fmt.Sprintf("the message role '%v'", fields["role"]))
	}
	switch content := fields["content"].(type) {
	case string:
		return nil
	case []any:
		for _, part := range content {
			p, ok := part.(map[string]any)
			if !ok || p["type"] != "text" {
				return domain.ErrGuestNotAllowed("images, audio or files")
			}
			if _, ok := p["text"].(string); !ok || len(p) != 2 {
				return domain.ErrGuestNotAllowed("this message content")
			}
		}
		return nil
	default:
		return domain.ErrInvalidField(fmt.Sprintf("message[%d] content must be text", i))
	}
}
