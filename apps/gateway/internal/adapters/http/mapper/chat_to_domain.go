package mapper

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/dto"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// unsupportedChatFields are known top-level fields that must be rejected
// because they change semantics in ways the gateway cannot support.
var unsupportedChatFields = map[string]bool{
	"best_of":       true,
	"function_call": true, "functions": true,
}

// knownChatFields are allowed top-level fields.
var knownChatFields = map[string]bool{
	"model": true, "messages": true, "stream": true,
	"max_tokens": true, "max_completion_tokens": true, "n": true,
	"temperature": true, "reasoning_effort": true, "top_p": true, "stop": true,
	"tools": true, "tool_choice": true, "parallel_tool_calls": true,
	"user": true, "metadata": true, "response_format": true,
	"provider_options": true, "stream_options": true,
	// Passed through / silently ignored:
	"presence_penalty": true, "frequency_penalty": true, "logit_bias": true,
	"seed": true, "logprobs": true, "top_logprobs": true,
	"suffix": true, "echo": true, "service_tier": true, "store": true,
}

// ChatCompletionRequestToDomain maps a /v1/chat/completions request DTO to the canonical GenerateRequest.
func ChatCompletionRequestToDomain(raw json.RawMessage) (domain.GenerateRequest, error) {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return domain.GenerateRequest{}, domain.ErrInvalidField("invalid JSON body")
	}

	for key := range rawMap {
		if unsupportedChatFields[key] {
			return domain.GenerateRequest{}, domain.ErrInvalidField(fmt.Sprintf("field '%s' is not supported on /v1/chat/completions in this version", key))
		}
		// Unknown fields are silently ignored for client compatibility
	}

	var req dto.ChatCompletionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return domain.GenerateRequest{}, domain.ErrInvalidField("invalid request body: " + err.Error())
	}

	if req.Model == "" {
		return domain.GenerateRequest{}, domain.ErrInvalidField("model is required")
	}

	if len(req.Messages) == 0 {
		return domain.GenerateRequest{}, domain.ErrInvalidField("messages is required and cannot be empty")
	}

	if req.MaxTokens != nil && req.MaxCompletionTokens != nil {
		return domain.GenerateRequest{}, domain.ErrInvalidField("cannot provide both max_tokens and max_completion_tokens")
	}
	if req.N != nil && *req.N != 1 {
		return domain.GenerateRequest{}, domain.ErrInvalidField("field 'n' only supports value 1 on /v1/chat/completions in this version")
	}

	input, err := mapChatMessages(req.Messages)
	if err != nil {
		return domain.GenerateRequest{}, err
	}

	gen := domain.GenerateRequest{
		PublicModelID:    req.Model,
		Input:            input,
		Temperature:      req.Temperature,
		ReasoningEffort:  req.ReasoningEffort,
		TopP:             req.TopP,
		Stream:           req.Stream != nil && *req.Stream,
		User:             req.User,
		Metadata:         req.Metadata,
		ProviderOptions:  req.ProviderOptions,
		PresencePenalty:  req.PresencePenalty,
		FrequencyPenalty: req.FrequencyPenalty,
		LogitBias:        req.LogitBias,
		Seed:             req.Seed,
		Logprobs:         req.Logprobs,
		TopLogprobs:      req.TopLogprobs,
		Store:            req.Store,
		ServiceTier:      req.ServiceTier,
	}

	// Normalize max tokens
	if req.MaxTokens != nil {
		gen.MaxOutputTokens = req.MaxTokens
	} else if req.MaxCompletionTokens != nil {
		gen.MaxOutputTokens = req.MaxCompletionTokens
	}

	if req.ParallelToolCalls != nil {
		gen.ParallelToolCalls = req.ParallelToolCalls
	}

	// Parse stop
	if len(req.Stop) > 0 {
		stops, err := parseStop(req.Stop)
		if err != nil {
			return domain.GenerateRequest{}, err
		}
		gen.Stop = stops
	}

	// Map tools
	for _, t := range req.Tools {
		td, err := mapToolDefinition(t)
		if err != nil {
			return domain.GenerateRequest{}, err
		}
		gen.Tools = append(gen.Tools, td)
	}

	// Map tool_choice
	if len(req.ToolChoice) > 0 {
		tc, err := parseToolChoice(req.ToolChoice)
		if err != nil {
			return domain.GenerateRequest{}, err
		}
		gen.ToolChoice = tc
	}

	// Map response_format -> TextConfig
	if req.ResponseFormat != nil {
		gen.TextConfig = &domain.ResponseTextConfig{
			FormatType: &req.ResponseFormat.Type,
			JSONSchema: req.ResponseFormat.JSONSchema,
		}
	}

	return gen, nil
}

// UnknownChatCompletionFields returns top-level request fields the gateway does
// not currently understand. The mapper ignores these fields for client
// compatibility, but handlers can log them for visibility.
func UnknownChatCompletionFields(raw json.RawMessage) []string {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return nil
	}

	fields := make([]string, 0)
	for key := range rawMap {
		if knownChatFields[key] || unsupportedChatFields[key] {
			continue
		}
		fields = append(fields, key)
	}
	sort.Strings(fields)
	return fields
}

func mapChatMessages(messages []dto.ChatMessage) ([]domain.InputItem, error) {
	var result []domain.InputItem

	for i, msg := range messages {
		switch msg.Role {
		case "system", "developer", "user":
			if msg.ToolCalls != nil {
				return nil, domain.ErrInvalidField(fmt.Sprintf("message[%d]: %s message must not contain tool_calls", i, msg.Role))
			}
			if msg.ToolCallID != nil {
				return nil, domain.ErrInvalidField(fmt.Sprintf("message[%d]: %s message must not contain tool_call_id", i, msg.Role))
			}
			content, err := extractStringContent(msg.Content)
			if err != nil {
				return nil, domain.ErrInvalidField(fmt.Sprintf("message[%d]: %s", i, err.Error()))
			}
			role := msg.Role
			result = append(result, domain.InputItem{
				Type:    domain.InputItemTypeMessage,
				Role:    &role,
				Content: &content,
			})

		case "assistant":
			if msg.ToolCallID != nil {
				return nil, domain.ErrInvalidField(fmt.Sprintf("message[%d]: assistant message must not contain tool_call_id", i))
			}
			role := msg.Role
			item := domain.InputItem{
				Type: domain.InputItemTypeMessage,
				Role: &role,
			}
			// May have content
			if len(msg.Content) > 0 {
				content, err := extractStringContent(msg.Content)
				if err == nil {
					item.Content = &content
				}
			}
			if msg.ReasoningContent != nil {
				item.ReasoningContent = msg.ReasoningContent
			}
			// May have tool_calls
			for _, tc := range msg.ToolCalls {
				item.ToolCalls = append(item.ToolCalls, domain.ToolCall{
					ID:            tc.ID,
					Name:          tc.Function.Name,
					ArgumentsJSON: tc.Function.Arguments,
				})
			}
			result = append(result, item)

		case "tool":
			if msg.ToolCallID == nil || *msg.ToolCallID == "" {
				return nil, domain.ErrToolMessageInvalid(fmt.Sprintf("message[%d]: tool message requires tool_call_id", i))
			}
			if msg.ToolCalls != nil {
				return nil, domain.ErrInvalidField(fmt.Sprintf("message[%d]: tool message must not contain tool_calls", i))
			}
			content, err := extractStringContent(msg.Content)
			if err != nil {
				return nil, domain.ErrInvalidField(fmt.Sprintf("message[%d]: %s", i, err.Error()))
			}
			role := msg.Role
			result = append(result, domain.InputItem{
				Type:       domain.InputItemTypeMessage,
				Role:       &role,
				Content:    &content,
				ToolCallID: msg.ToolCallID,
			})

		default:
			return nil, domain.ErrInvalidField(fmt.Sprintf("message[%d]: unsupported role '%s'", i, msg.Role))
		}
	}

	return result, nil
}

func extractStringContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("content is required")
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}

	// Try array of content parts
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("content must be a string or array of text content parts")
	}

	var combined string
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text", "output_text":
			combined += p.Text
		default:
			// Skip non-text content parts for now
		}
	}
	return combined, nil
}

func parseStop(raw json.RawMessage) ([]string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}, nil
	}

	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, domain.ErrInvalidField("stop must be a string or array of strings")
	}
	return arr, nil
}

func mapToolDefinition(t dto.ToolDefinition) (domain.ToolDefinition, error) {
	if t.Type != "" && t.Type != "function" {
		return domain.ToolDefinition{}, domain.ErrInvalidField(fmt.Sprintf("unsupported tool type: %s", t.Type))
	}
	if t.Function.Name == "" {
		return domain.ToolDefinition{}, domain.ErrInvalidField("tool function name is required")
	}
	strict := false
	if t.Function.Strict != nil {
		strict = *t.Function.Strict
	}
	return domain.ToolDefinition{
		Name:        t.Function.Name,
		Description: t.Function.Description,
		Parameters:  t.Function.Parameters,
		Strict:      strict,
	}, nil
}

func parseToolChoice(raw json.RawMessage) (*domain.ToolChoice, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "none", "auto", "required":
			return &domain.ToolChoice{Mode: s}, nil
		default:
			return nil, domain.ErrInvalidField(fmt.Sprintf("invalid tool_choice value: %s", s))
		}
	}

	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, domain.ErrInvalidField("tool_choice must be a string or object")
	}

	if obj.Type != "function" {
		return nil, domain.ErrInvalidField(fmt.Sprintf("unsupported tool_choice type: %s", obj.Type))
	}
	if obj.Function.Name == "" {
		return nil, domain.ErrInvalidField("tool_choice function name is required")
	}
	return &domain.ToolChoice{
		Mode:         domain.ToolChoiceFunction,
		FunctionName: &obj.Function.Name,
	}, nil
}
