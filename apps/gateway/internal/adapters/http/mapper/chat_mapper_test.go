package mapper_test

import (
	"encoding/json"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/mapper"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestChatCompletionRequestToDomain_Basic(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "openai/gpt-4.1-mini",
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`)

	req, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.PublicModelID != "openai/gpt-4.1-mini" {
		t.Errorf("model = %s, want openai/gpt-4.1-mini", req.PublicModelID)
	}
	if len(req.Input) != 1 {
		t.Fatalf("input len = %d, want 1", len(req.Input))
	}
	if *req.Input[0].Content != "Hello" {
		t.Errorf("content = %s, want Hello", *req.Input[0].Content)
	}
	if *req.Input[0].Role != "user" {
		t.Errorf("role = %s, want user", *req.Input[0].Role)
	}
}

func TestChatCompletionRequestToDomain_DeveloperRole(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "openai/gpt-5",
		"messages": [
			{"role": "developer", "content": "Follow these instructions"}
		]
	}`)

	req, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Input) != 1 {
		t.Fatalf("input len = %d, want 1", len(req.Input))
	}
	if *req.Input[0].Role != "developer" {
		t.Fatalf("role = %s, want developer", *req.Input[0].Role)
	}
	if *req.Input[0].Content != "Follow these instructions" {
		t.Fatalf("content = %s, want developer content", *req.Input[0].Content)
	}
}

func TestChatCompletionRequestToDomain_EmptyMessages(t *testing.T) {
	raw := json.RawMessage(`{"model": "test", "messages": []}`)
	_, err := mapper.ChatCompletionRequestToDomain(raw)
	if err == nil {
		t.Fatal("expected error for empty messages")
	}
}

func TestChatCompletionRequestToDomain_BothMaxTokens(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"max_tokens": 100,
		"max_completion_tokens": 200
	}`)
	_, err := mapper.ChatCompletionRequestToDomain(raw)
	if err == nil {
		t.Fatal("expected error when both max_tokens and max_completion_tokens are set")
	}
}

func TestChatCompletionRequestToDomain_MaxTokensOnly(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"max_tokens": 100
	}`)
	req, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.MaxOutputTokens == nil || *req.MaxOutputTokens != 100 {
		t.Errorf("max_output_tokens = %v, want 100", req.MaxOutputTokens)
	}
}

func TestChatCompletionRequestToDomain_MaxCompletionTokensOnly(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"max_completion_tokens": 200
	}`)
	req, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.MaxOutputTokens == nil || *req.MaxOutputTokens != 200 {
		t.Errorf("max_output_tokens = %v, want 200", req.MaxOutputTokens)
	}
}

func TestChatCompletionRequestToDomain_ReasoningEffort(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"reasoning_effort": "low"
	}`)
	req, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.ReasoningEffort == nil || *req.ReasoningEffort != "low" {
		t.Errorf("reasoning_effort = %v, want low", req.ReasoningEffort)
	}
	if fields := mapper.UnknownChatCompletionFields(raw); len(fields) != 0 {
		t.Fatalf("unknown fields = %v, want none", fields)
	}
}

func TestChatCompletionRequestToDomain_UnknownField(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"foobar": true
	}`)
	_, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unknown fields should be silently ignored, got: %v", err)
	}
}

func TestUnknownChatCompletionFields(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"zeta": true,
		"n": 1,
		"best_of": 2,
		"alpha": "ignored"
	}`)
	fields := mapper.UnknownChatCompletionFields(raw)
	if len(fields) != 2 || fields[0] != "alpha" || fields[1] != "zeta" {
		t.Fatalf("fields = %v, want [alpha zeta]", fields)
	}
}

func TestChatCompletionRequestToDomain_NOne(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"n": 1
	}`)
	_, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error for n=1: %v", err)
	}
}

func TestChatCompletionRequestToDomain_NMultipleUnsupported(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"n": 2
	}`)
	_, err := mapper.ChatCompletionRequestToDomain(raw)
	if err == nil {
		t.Fatal("expected error for n > 1")
	}
}

func TestChatCompletionRequestToDomain_ToolMessage(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [
			{"role": "user", "content": "What is the weather?"},
			{"role": "assistant", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"location\":\"NYC\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "72F sunny"}
		]
	}`)

	req, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Input) != 3 {
		t.Fatalf("input len = %d, want 3", len(req.Input))
	}
	if len(req.Input[1].ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls len = %d, want 1", len(req.Input[1].ToolCalls))
	}
	if *req.Input[2].ToolCallID != "call_1" {
		t.Errorf("tool_call_id = %s, want call_1", *req.Input[2].ToolCallID)
	}
}

func TestChatCompletionRequestToDomain_AssistantReasoningContent(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [
			{"role": "assistant", "reasoning_content": "thoughts", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "lookup", "arguments": "{}"}}]}
		]
	}`)

	req, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Input[0].ReasoningContent == nil || *req.Input[0].ReasoningContent != "thoughts" {
		t.Fatalf("reasoning_content = %v, want thoughts", req.Input[0].ReasoningContent)
	}
}

func TestChatCompletionRequestToDomain_ToolMessageMissingID(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [
			{"role": "tool", "content": "result"}
		]
	}`)
	_, err := mapper.ChatCompletionRequestToDomain(raw)
	if err == nil {
		t.Fatal("expected error for tool message without tool_call_id")
	}
}

func TestChatCompletionRequestToDomain_SystemWithToolCalls(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [
			{"role": "system", "content": "You are helpful", "tool_calls": [{"id": "x", "type": "function", "function": {"name": "f", "arguments": "{}"}}]}
		]
	}`)
	_, err := mapper.ChatCompletionRequestToDomain(raw)
	if err == nil {
		t.Fatal("expected error for system message with tool_calls")
	}
}

func TestChatCompletionRequestToDomain_Stop(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"stop": ["END", "STOP"]
	}`)
	req, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Stop) != 2 {
		t.Errorf("stop len = %d, want 2", len(req.Stop))
	}
}

func TestChatCompletionRequestToDomain_StopString(t *testing.T) {
	raw := json.RawMessage(`{
		"model": "test",
		"messages": [{"role": "user", "content": "hi"}],
		"stop": "END"
	}`)
	req, err := mapper.ChatCompletionRequestToDomain(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Stop) != 1 || req.Stop[0] != "END" {
		t.Errorf("stop = %v, want [END]", req.Stop)
	}
}

func TestDomainToChatCompletionResponse(t *testing.T) {
	content := "Hello!"
	reasoning := "thinking"
	role := "assistant"
	finishReason := "stop"
	result := domain.GenerateResult{
		ID:            "test-id-123",
		CreatedUnix:   1700000000,
		PublicModelID: "openai/gpt-4.1-mini",
		Output: []domain.OutputItem{
			{Type: "message", Role: &role, Content: &content, ReasoningContent: &reasoning},
		},
		FinishReason: &finishReason,
		Usage: &domain.Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}

	resp := mapper.DomainToChatCompletionResponse(result)
	if resp.Object != "chat.completion" {
		t.Errorf("object = %s, want chat.completion", resp.Object)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices len = %d, want 1", len(resp.Choices))
	}
	if *resp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("content = %s, want Hello!", *resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].Message.ReasoningContent == nil || *resp.Choices[0].Message.ReasoningContent != "thinking" {
		t.Errorf("reasoning_content = %v, want thinking", resp.Choices[0].Message.ReasoningContent)
	}
	if *resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %s, want stop", *resp.Choices[0].FinishReason)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("total_tokens = %d, want 15", resp.Usage.TotalTokens)
	}
}
