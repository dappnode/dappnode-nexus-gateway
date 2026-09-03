package domain

// ToolDefinition describes a function tool available to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
	Strict      bool
}

// ToolCall represents a tool invocation returned by the model.
type ToolCall struct {
	ID            string
	Name          string
	ArgumentsJSON string
}

// ToolCallDelta represents a partial tool call in a stream event.
type ToolCallDelta struct {
	Index          int
	ID             *string
	Name           *string
	ArgumentsDelta *string
}

// ToolChoiceMode constants.
const (
	ToolChoiceNone     = "none"
	ToolChoiceAuto     = "auto"
	ToolChoiceRequired = "required"
	ToolChoiceFunction = "function"
)

// ToolChoice represents the tool_choice parameter.
type ToolChoice struct {
	Mode         string  // "none", "auto", "required", "function"
	FunctionName *string // set only when Mode == "function"
}
