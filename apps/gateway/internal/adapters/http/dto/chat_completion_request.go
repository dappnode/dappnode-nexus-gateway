package dto

import "encoding/json"

// ChatCompletionRequest is the DTO for POST /v1/chat/completions.
type ChatCompletionRequest struct {
	Model               string           `json:"model"`
	Messages            []ChatMessage    `json:"messages"`
	Stream              *bool            `json:"stream,omitempty"`
	MaxTokens           *int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int             `json:"max_completion_tokens,omitempty"`
	N                   *int             `json:"n,omitempty"`
	Temperature         *float64         `json:"temperature,omitempty"`
	ReasoningEffort     *string          `json:"reasoning_effort,omitempty"`
	TopP                *float64         `json:"top_p,omitempty"`
	Stop                json.RawMessage  `json:"stop,omitempty"`
	Tools               []ToolDefinition `json:"tools,omitempty"`
	ToolChoice          json.RawMessage  `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool            `json:"parallel_tool_calls,omitempty"`
	User                *string          `json:"user,omitempty"`
	Metadata            map[string]any   `json:"metadata,omitempty"`
	ResponseFormat      *ResponseFormat  `json:"response_format,omitempty"`
	ProviderOptions     map[string]any   `json:"provider_options,omitempty"`
	PresencePenalty     *float64         `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64         `json:"frequency_penalty,omitempty"`
	LogitBias           map[string]int   `json:"logit_bias,omitempty"`
	Seed                *int             `json:"seed,omitempty"`
	Logprobs            *bool            `json:"logprobs,omitempty"`
	TopLogprobs         *int             `json:"top_logprobs,omitempty"`
	Store               *bool            `json:"store,omitempty"`
	ServiceTier         *string          `json:"service_tier,omitempty"`
}

// ChatMessage is a message in the chat completions format.
type ChatMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       *string         `json:"tool_call_id,omitempty"`
	Name             *string         `json:"name,omitempty"`
}

// ChatToolCall is a tool call in assistant messages.
type ChatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ChatFunctionCall `json:"function"`
}

// ChatFunctionCall holds the function call details.
type ChatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ResponseFormat for structured outputs.
type ResponseFormat struct {
	Type       string         `json:"type"`
	JSONSchema map[string]any `json:"json_schema,omitempty"`
}

// ToolDefinition describes a tool in the chat completions format.
type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction holds the function details inside a ToolDefinition.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}
