package domain

// Endpoint constants.
const (
	EndpointChatCompletions = "chat_completions"
)

// GenerateRequest is what the gateway reads from a client's request: enough
// to route, check the model's features, and meter. The request body itself is
// forwarded to the provider as sent.
type GenerateRequest struct {
	PublicModelID    string
	RequestedModelID string
	// Routing decision metadata. Set when the request was resolved via a
	// router; nil for direct model requests.
	RouterID              *string
	RoutedPublicModelID   *string
	MatchedCategory       *string
	RoutingScore          *float32
	RoutingCategoryScores []RoutingCategoryScore
	DecisionReason        *string
	FallbackUsed          *bool

	// Input and Tools are what routers read: each message's role and text,
	// and the tool names.
	Input []InputItem
	Tools []ToolDefinition

	Stream            bool
	MaxOutputTokens   *int
	ServiceTier       *string
	ParallelToolCalls *bool
	// StructuredOutput is set when the client asks for JSON output.
	StructuredOutput bool
}

// InputItem is one message: its role and text.
type InputItem struct {
	Role    *string
	Content *string
}

// ToolDefinition names a function tool offered to the model.
type ToolDefinition struct {
	Name string
}

// GenerateResult is what the gateway read from a provider's response.
type GenerateResult struct {
	ID              string
	PublicModelID   string
	ProviderName    string
	ProviderModelID string
	FinishReason    *string
	Usage           *Usage
	TinfoilProof    *TinfoilTransportProof
}
