package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImageTokenUsageValidation(t *testing.T) {
	raw := `{"input_tokens":3000,"output_tokens":4000,"total_tokens":7000,"generated_images":1,"input_tokens_details":{"text_tokens":1000,"image_tokens":2000},"output_tokens_details":{"image_tokens":4000}}`
	usage, ok := ParseImageTokenUsage(json.RawMessage(raw), 1)
	if !ok || usage.PromptTokens != 1000 || usage.ImageInputTokens != 2000 || usage.CompletionTokens != 4000 || usage.TotalTokens != 7000 {
		t.Fatalf("usage=%+v valid=%v", usage, ok)
	}
	for _, invalid := range []string{`{}`, `null`, strings.Replace(raw, `"text_tokens":1000`, `"text_tokens":-1`, 1), strings.Replace(raw, `"input_tokens":3000`, `"input_tokens":2000`, 1), strings.Replace(raw, `"generated_images":1`, `"generated_images":2`, 1), strings.Replace(raw, `"image_tokens":2000`, `"image_tokens":null`, 1)} {
		if _, ok := ParseImageTokenUsage(json.RawMessage(invalid), 1); ok {
			t.Fatalf("accepted invalid usage: %s", invalid)
		}
	}
}
