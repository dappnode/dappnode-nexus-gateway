package services

import (
	"bytes"
	"encoding/json"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Metering holds credit for a request's prompt before the provider runs it.
// The hold needs an upper bound, not a guess: the prompt is charged at its
// real size when the request settles, and an estimate that is too low lets a
// request start that the account may not be able to pay for.
//
// The bound is the body's size in bytes. The tokenizers behind the models
// Nexus serves (byte-level BPE, and SentencePiece with byte fallback) never
// produce more tokens than the UTF-8 bytes of the text they encode. The JSON
// body carries every prompt string at least once, plus quotes, keys and
// escapes, so its length also covers the role markers the chat template adds
// per message. promptOverheadTokens covers what a provider adds that is not in
// the body: rendered tool instructions, a default system prompt, the upstream
// model name.
//
// Media is not text. An image part counts imageTokens instead of its bytes: an
// inline base64 image is far longer in bytes than in tokens, and a linked one
// far shorter. Audio, video, files, and part types the gateway does not know
// have no bound it can compute, so those requests get no estimate and the hold
// covers the model's whole context window, as it did before.
const (
	// imageTokens is above what any model Nexus serves spends on one image.
	imageTokens          = 16_384
	promptOverheadTokens = 1_024
)

// estimateInputTokens returns an upper bound on the prompt tokens of body,
// the request exactly as it goes upstream. ok is false when there is no bound.
func estimateInputTokens(endpoint string, body []byte) (tokens int64, ok bool) {
	// SystemOne's provider builds its own prompt from the request, so its
	// size is unknown here.
	if endpoint != domain.EndpointChatCompletions {
		return 0, false
	}
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0, false
	}
	tokens = int64(len(body)) + promptOverheadTokens
	for _, message := range req.Messages {
		content := bytes.TrimSpace(message.Content)
		if len(content) == 0 || content[0] != '[' {
			continue // A string or null: text, already counted.
		}
		var parts []json.RawMessage
		if err := json.Unmarshal(content, &parts); err != nil {
			return 0, false
		}
		for _, part := range parts {
			var p struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(part, &p); err != nil {
				return 0, false
			}
			switch p.Type {
			case "text", "input_text", "output_text", "refusal":
				// Text, already counted in the body's bytes.
			case "image_url", "image", "input_image":
				tokens += imageTokens - int64(len(part))
			default:
				return 0, false
			}
		}
	}
	return max(tokens, 1), true
}
