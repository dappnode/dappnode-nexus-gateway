package domain

// StreamEventType constants.
const (
	StreamEventOutputTextDelta    = "output_text_delta"
	StreamEventOutputMessageDelta = "output_message_delta"
	StreamEventToolCallDelta      = "tool_call_delta"
	StreamEventCompleted          = "completed"
	StreamEventError              = "error"
)

// StreamEvent is a canonical stream event normalized from provider stream formats.
type StreamEvent struct {
	Type               string // output_text_delta, output_message_delta, tool_call_delta, completed, error
	ProviderResponseID string
	ChoiceIndex        *int
	Role               *string
	ContentDelta       *string
	ReasoningDelta     *string
	ToolCallDelta      *ToolCallDelta
	FinishReason       *string
	Usage              *Usage
	Error              *GatewayError
}
