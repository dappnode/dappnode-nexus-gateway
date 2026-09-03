package mapper

import (
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/dto"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// DomainToChatCompletionResponse maps a canonical GenerateResult to a chat completions response DTO.
func DomainToChatCompletionResponse(result domain.GenerateResult) dto.ChatCompletionResponse {
	resp := dto.ChatCompletionResponse{
		ID:      chatCompletionID(result.ID),
		Object:  "chat.completion",
		Created: result.CreatedUnix,
		Model:   result.PublicModelID,
	}

	message := dto.ChatChoiceMessage{
		Role: "assistant",
	}

	for _, out := range result.Output {
		if out.Content != nil && *out.Content != "" {
			message.Content = out.Content
		}
		if out.ReasoningContent != nil && *out.ReasoningContent != "" {
			message.ReasoningContent = out.ReasoningContent
		}
		for _, tc := range out.ToolCalls {
			message.ToolCalls = append(message.ToolCalls, dto.ChatToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: dto.ChatFunctionCall{
					Name:      tc.Name,
					Arguments: tc.ArgumentsJSON,
				},
			})
		}
	}

	choice := dto.ChatCompletionChoice{
		Index:        0,
		Message:      message,
		FinishReason: result.FinishReason,
	}

	resp.Choices = []dto.ChatCompletionChoice{choice}

	if result.Usage != nil {
		resp.Usage = &dto.ChatCompletionUsage{
			PromptTokens:     result.Usage.PromptTokens,
			CompletionTokens: result.Usage.CompletionTokens,
			TotalTokens:      result.Usage.TotalTokens,
		}
	}

	return resp
}

// DomainStreamEventToChatChunk maps a canonical StreamEvent to a chat completions streaming chunk.
func DomainStreamEventToChatChunk(event domain.StreamEvent, model string, responseID string, createdAt int64) (data *dto.ChatCompletionChunk, done bool) {
	chunk := &dto.ChatCompletionChunk{
		ID:      chatCompletionID(responseID),
		Object:  "chat.completion.chunk",
		Created: createdAt,
		Model:   model,
	}

	switch event.Type {
	case domain.StreamEventOutputTextDelta:
		choice := dto.ChatCompletionChunkChoice{
			Index: 0,
			Delta: dto.ChatChunkDelta{
				Content:          event.ContentDelta,
				ReasoningContent: event.ReasoningDelta,
			},
		}
		if event.Role != nil {
			choice.Delta.Role = *event.Role
		}
		chunk.Choices = []dto.ChatCompletionChunkChoice{choice}
		return chunk, false

	case domain.StreamEventOutputMessageDelta:
		choice := dto.ChatCompletionChunkChoice{
			Index: 0,
			Delta: dto.ChatChunkDelta{},
		}
		if event.Role != nil {
			choice.Delta.Role = *event.Role
		}
		chunk.Choices = []dto.ChatCompletionChunkChoice{choice}
		return chunk, false

	case domain.StreamEventToolCallDelta:
		choice := dto.ChatCompletionChunkChoice{
			Index: 0,
			Delta: dto.ChatChunkDelta{},
		}
		if event.ToolCallDelta != nil {
			tc := dto.ChatToolCallChunk{
				Index: event.ToolCallDelta.Index,
			}
			if event.ToolCallDelta.ID != nil {
				tc.ID = *event.ToolCallDelta.ID
				tc.Type = "function"
			}
			fn := dto.ChatFunctionCall{}
			if event.ToolCallDelta.Name != nil {
				fn.Name = *event.ToolCallDelta.Name
			}
			if event.ToolCallDelta.ArgumentsDelta != nil {
				fn.Arguments = *event.ToolCallDelta.ArgumentsDelta
			}
			tc.Function = fn
			choice.Delta.ToolCalls = []dto.ChatToolCallChunk{tc}
		}
		chunk.Choices = []dto.ChatCompletionChunkChoice{choice}
		return chunk, false

	case domain.StreamEventCompleted:
		delta := dto.ChatChunkDelta{}
		if event.ContentDelta != nil {
			delta.Content = event.ContentDelta
		}
		if event.ReasoningDelta != nil {
			delta.ReasoningContent = event.ReasoningDelta
		}
		choice := dto.ChatCompletionChunkChoice{
			Index:        0,
			Delta:        delta,
			FinishReason: event.FinishReason,
		}
		chunk.Choices = []dto.ChatCompletionChunkChoice{choice}
		if event.Usage != nil {
			chunk.Usage = &dto.ChatCompletionUsage{
				PromptTokens:     event.Usage.PromptTokens,
				CompletionTokens: event.Usage.CompletionTokens,
				TotalTokens:      event.Usage.TotalTokens,
			}
		}
		return chunk, true

	case domain.StreamEventError:
		return nil, true

	default:
		return nil, false
	}
}

func chatCompletionID(id string) string {
	if strings.HasPrefix(id, "chatcmpl-") {
		return id
	}
	return "chatcmpl-" + id
}
