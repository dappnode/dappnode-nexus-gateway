package domain

import "encoding/json"

// Usage holds token usage information for a generation.
type Usage struct {
	ProviderUsage       json.RawMessage `json:"provider_usage,omitempty"`
	PromptTokens        int64           `json:"prompt_tokens"`
	CompletionTokens    int64           `json:"completion_tokens"`
	TotalTokens         int64           `json:"total_tokens"`
	CacheCreationTokens int64           `json:"cache_creation_tokens"`
	CacheReadTokens     int64           `json:"cache_read_tokens"`
	// Images is how many images an image generation returned.
	Images int64 `json:"images,omitempty"`
}
