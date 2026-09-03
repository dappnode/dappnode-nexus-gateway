package domain

// InputItemType constants.
const (
	InputItemTypeMessage = "message"
	InputItemTypeText    = "text"
)

// InputItem is a canonical input item for generation requests.
type InputItem struct {
	Type             string // "message" or "text"
	Role             *string
	Content          *string
	ReasoningContent *string
	ToolCalls        []ToolCall
	ToolCallID       *string
}
