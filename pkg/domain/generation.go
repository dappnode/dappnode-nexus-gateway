package domain

// Endpoint constants.
const (
	EndpointChatCompletions = "chat_completions"
)

// ResponseTextConfig configures structured text output.
type ResponseTextConfig struct {
	FormatType *string
	JSONSchema map[string]any
}

// GenerateRequest is the canonical internal generation request.
type GenerateRequest struct {
	PublicModelID       string
	RequestedModelID    string
	RouterID            *string
	RoutedPublicModelID *string
	// Routing decision metadata. Set when the request was resolved via a
	// router; nil for direct model requests.
	MatchedCategory       *string
	RoutingScore          *float32
	RoutingCategoryScores []RoutingCategoryScore
	DecisionReason        *string
	FallbackUsed          *bool
	Input                 []InputItem
	Instructions          *string
	MaxOutputTokens       *int
	Temperature           *float64
	ReasoningEffort       *string
	TopP                  *float64
	Stop                  []string
	Stream                bool
	Tools                 []ToolDefinition
	ToolChoice            *ToolChoice
	ParallelToolCalls     *bool
	User                  *string
	Metadata              map[string]any
	TextConfig            *ResponseTextConfig
	ProviderOptions       map[string]any

	// Pass-through parameters (forwarded to providers that support them)
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int `json:"logit_bias,omitempty"`
	Seed             *int           `json:"seed,omitempty"`
	Logprobs         *bool          `json:"logprobs,omitempty"`
	TopLogprobs      *int           `json:"top_logprobs,omitempty"`
	Store            *bool          `json:"store,omitempty"`
	ServiceTier      *string        `json:"service_tier,omitempty"`
}

// GenerateResult is the canonical internal generation result.
type GenerateResult struct {
	ID              string
	CreatedUnix     int64
	PublicModelID   string
	ProviderName    string
	ProviderModelID string
	Output          []OutputItem
	FinishReason    *string
	Usage           *Usage
	TinfoilProof    *TinfoilTransportProof
}
