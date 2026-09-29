package mapper

import (
	"encoding/json"
	"fmt"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// unsupportedChatFields change semantics in ways the gateway cannot support.
var unsupportedChatFields = []string{"best_of", "function_call", "functions"}

// chatRequest is what the gateway reads from a chat-completions body. The
// body itself is forwarded as sent; this only validates it and extracts what
// routing, feature checks, and metering need.
type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	Stream              *bool           `json:"stream"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	N                   *int            `json:"n"`
	Tools               []chatTool      `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls"`
	ResponseFormat      *struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
	ToolCallID *string         `json:"tool_call_id"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

// ChatCompletionRequestToDomain validates a /v1/chat/completions body and
// reads the request summary the gateway works with: the model, streaming,
// the output limit, the features used, and the text of each message and the
// tool names for the router.
func ChatCompletionRequestToDomain(raw json.RawMessage) (domain.GenerateRequest, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return domain.GenerateRequest{}, domain.ErrInvalidField("invalid JSON body")
	}
	for _, key := range unsupportedChatFields {
		if _, ok := fields[key]; ok {
			return domain.GenerateRequest{}, domain.ErrInvalidField(fmt.Sprintf("field '%s' is not supported on /v1/chat/completions in this version", key))
		}
	}
	var req chatRequest
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

	gen := domain.GenerateRequest{
		PublicModelID:     req.Model,
		Stream:            req.Stream != nil && *req.Stream,
		MaxOutputTokens:   req.MaxTokens,
		ParallelToolCalls: req.ParallelToolCalls,
		StructuredOutput:  req.ResponseFormat != nil && (req.ResponseFormat.Type == "json_object" || req.ResponseFormat.Type == "json_schema"),
	}
	if gen.MaxOutputTokens == nil {
		gen.MaxOutputTokens = req.MaxCompletionTokens
	}
	for i, msg := range req.Messages {
		item, err := readMessage(i, msg)
		if err != nil {
			return domain.GenerateRequest{}, err
		}
		gen.Input = append(gen.Input, item)
	}
	for _, t := range req.Tools {
		if t.Type != "" && t.Type != "function" {
			return domain.GenerateRequest{}, domain.ErrInvalidField(fmt.Sprintf("unsupported tool type: %s", t.Type))
		}
		if t.Function.Name == "" {
			return domain.GenerateRequest{}, domain.ErrInvalidField("tool function name is required")
		}
		gen.Tools = append(gen.Tools, domain.ToolDefinition{Name: t.Function.Name})
	}
	if len(req.ToolChoice) > 0 {
		if err := checkToolChoice(req.ToolChoice); err != nil {
			return domain.GenerateRequest{}, err
		}
	}
	return gen, nil
}

// readMessage checks a message's shape and reads its role and text.
func readMessage(i int, msg chatMessage) (domain.InputItem, error) {
	hasToolCalls := len(msg.ToolCalls) > 0 && string(msg.ToolCalls) != "null"
	role := msg.Role
	item := domain.InputItem{Role: &role}
	switch msg.Role {
	case "system", "developer", "user", "tool":
		if hasToolCalls {
			return item, domain.ErrInvalidField(fmt.Sprintf("message[%d]: %s message must not contain tool_calls", i, msg.Role))
		}
		if msg.Role == "tool" && (msg.ToolCallID == nil || *msg.ToolCallID == "") {
			return item, domain.ErrToolMessageInvalid(fmt.Sprintf("message[%d]: tool message requires tool_call_id", i))
		}
		if msg.Role != "tool" && msg.ToolCallID != nil {
			return item, domain.ErrInvalidField(fmt.Sprintf("message[%d]: %s message must not contain tool_call_id", i, msg.Role))
		}
		text, err := textContent(msg.Content)
		if err != nil {
			return item, domain.ErrInvalidField(fmt.Sprintf("message[%d]: %s", i, err.Error()))
		}
		item.Content = &text
	case "assistant":
		if msg.ToolCallID != nil {
			return item, domain.ErrInvalidField(fmt.Sprintf("message[%d]: assistant message must not contain tool_call_id", i))
		}
		if text, err := textContent(msg.Content); err == nil {
			item.Content = &text
		}
	default:
		return item, domain.ErrInvalidField(fmt.Sprintf("message[%d]: unsupported role '%s'", i, msg.Role))
	}
	return item, nil
}

// textContent reads a message's text: a string, or the text parts of an
// array (images and other parts are forwarded, but carry no text).
func textContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("content is required")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("content must be a string or array of content parts")
	}
	var text string
	for _, p := range parts {
		if p.Type == "text" || p.Type == "input_text" || p.Type == "output_text" {
			text += p.Text
		}
	}
	return text, nil
}

func checkToolChoice(raw json.RawMessage) error {
	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil {
		if mode == "none" || mode == "auto" || mode == "required" {
			return nil
		}
		return domain.ErrInvalidField(fmt.Sprintf("invalid tool_choice value: %s", mode))
	}
	var named struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &named); err != nil {
		return domain.ErrInvalidField("tool_choice must be a string or object")
	}
	if named.Type != "function" {
		return domain.ErrInvalidField(fmt.Sprintf("unsupported tool_choice type: %s", named.Type))
	}
	if named.Function.Name == "" {
		return domain.ErrInvalidField("tool_choice function name is required")
	}
	return nil
}
