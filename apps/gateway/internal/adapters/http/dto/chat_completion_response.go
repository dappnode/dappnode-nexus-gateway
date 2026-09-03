package dto

// ChatCompletionResponse is the non-streaming response for POST /v1/chat/completions.
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   *ChatCompletionUsage   `json:"usage,omitempty"`
}

// ChatCompletionChoice is a single choice in the response.
type ChatCompletionChoice struct {
	Index        int               `json:"index"`
	Message      ChatChoiceMessage `json:"message"`
	FinishReason *string           `json:"finish_reason"`
}

// ChatChoiceMessage is the assistant message in a choice.
type ChatChoiceMessage struct {
	Role             string         `json:"role"`
	Content          *string        `json:"content"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatToolCall `json:"tool_calls,omitempty"`
}

// ChatCompletionUsage holds token usage for chat completions.
type ChatCompletionUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ChatCompletionChunk is a streaming chunk for chat completions.
type ChatCompletionChunk struct {
	ID      string                      `json:"id"`
	Object  string                      `json:"object"`
	Created int64                       `json:"created"`
	Model   string                      `json:"model"`
	Choices []ChatCompletionChunkChoice `json:"choices"`
	Usage   *ChatCompletionUsage        `json:"usage,omitempty"`
}

// ChatCompletionChunkChoice is a streaming choice delta.
type ChatCompletionChunkChoice struct {
	Index        int            `json:"index"`
	Delta        ChatChunkDelta `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

// ChatChunkDelta holds the delta content in a streaming chunk.
type ChatChunkDelta struct {
	Role             string              `json:"role,omitempty"`
	Content          *string             `json:"content,omitempty"`
	ReasoningContent *string             `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatToolCallChunk `json:"tool_calls,omitempty"`
}

// ChatToolCallChunk is a partial tool call in a stream chunk.
type ChatToolCallChunk struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ChatFunctionCall `json:"function"`
}
