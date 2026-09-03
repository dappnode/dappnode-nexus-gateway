package anthropic

import (
	"encoding/json"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func buildRequestBody(req domain.GenerateRequest, model domain.PublicModel) map[string]any {
	body := map[string]any{
		"model": model.UpstreamModelName,
	}

	messages, system := buildMessages(req)
	body["messages"] = messages
	if system != "" {
		body["system"] = system
	}

	if req.MaxOutputTokens != nil {
		v := *req.MaxOutputTokens
		if model.MaxOutputTokens > 0 && v > model.MaxOutputTokens {
			v = model.MaxOutputTokens
		}
		body["max_tokens"] = v
	} else {
		body["max_tokens"] = model.MaxOutputTokens
	}

	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		body["stop_sequences"] = req.Stop
	}
	if req.Stream {
		body["stream"] = true
	}

	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tool := map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.Parameters,
			}
			tools = append(tools, tool)
		}
		body["tools"] = tools
	}

	if req.ToolChoice != nil {
		switch req.ToolChoice.Mode {
		case domain.ToolChoiceAuto:
			body["tool_choice"] = map[string]any{"type": "auto"}
		case domain.ToolChoiceRequired:
			body["tool_choice"] = map[string]any{"type": "any"}
		case domain.ToolChoiceFunction:
			body["tool_choice"] = map[string]any{
				"type": "tool",
				"name": *req.ToolChoice.FunctionName,
			}
		case domain.ToolChoiceNone:
			delete(body, "tools")
		}
	}

	return body
}

func buildMessages(req domain.GenerateRequest) ([]map[string]any, string) {
	var messages []map[string]any
	var system string

	if req.Instructions != nil && *req.Instructions != "" {
		system = *req.Instructions
	}

	for _, item := range req.Input {
		role := "user"
		if item.Role != nil {
			role = *item.Role
		}

		if role == "system" || role == "developer" {
			if item.Content != nil {
				if system != "" {
					system += "\n"
				}
				system += *item.Content
			}
			continue
		}

		msg := map[string]any{
			"role": role,
		}

		if role == "tool" && item.ToolCallID != nil {
			msg["role"] = "user"
			msg["content"] = []map[string]any{
				{
					"type":        "tool_result",
					"tool_use_id": *item.ToolCallID,
					"content":     safeContent(item.Content),
				},
			}
		} else if len(item.ToolCalls) > 0 {
			content := make([]map[string]any, 0)
			if item.Content != nil && *item.Content != "" {
				content = append(content, map[string]any{
					"type": "text",
					"text": *item.Content,
				})
			}
			for _, tc := range item.ToolCalls {
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": parseJSONOrString(tc.ArgumentsJSON),
				})
			}
			msg["content"] = content
		} else if item.Content != nil {
			msg["content"] = *item.Content
		}

		messages = append(messages, msg)
	}

	return messages, system
}

func safeContent(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func parseJSONOrString(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}
