package domain

// OutputItemType constants.
const (
	OutputItemTypeMessage = "message"
	OutputItemTypeText    = "text"
)

// OutputItem is a canonical output item from a generation result.
type OutputItem struct {
	Type             string // "message" or "text"
	Role             *string
	Content          *string
	ReasoningContent *string
	ToolCalls        []ToolCall
}
