package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestBuildProviderRequest_DefaultPolicyTokenLimit(t *testing.T) {
	maxTokens := 32
	req := domain.GenerateRequest{
		PublicModelID:   "openai/test",
		Input:           []domain.InputItem{message("user", "hi")},
		MaxOutputTokens: &maxTokens,
	}
	model := domain.PublicModel{
		UpstreamModelName: "test-model",
		ProviderConfig:    domain.ProviderConfig{ProviderName: "openai"},
		MaxOutputTokens:   128,
	}

	built := buildProviderRequest(req, model)
	if built.Policy != "openai-compatible" {
		t.Fatalf("policy = %q, want openai-compatible", built.Policy)
	}
	if got := built.Body["max_completion_tokens"]; got != 32 {
		t.Fatalf("max_completion_tokens = %v, want 32", got)
	}
	if _, ok := built.Body["max_tokens"]; ok {
		t.Fatal("default policy must not send max_tokens")
	}
}

func TestBuildProviderRequest_ForwardsReasoningEffort(t *testing.T) {
	effort := "low"
	maxTokens := 1024
	req := domain.GenerateRequest{
		PublicModelID:   "phala/gpt-oss-20b",
		Input:           []domain.InputItem{message("user", "hi")},
		ReasoningEffort: &effort,
		MaxOutputTokens: &maxTokens,
	}
	model := domain.PublicModel{
		UpstreamModelName: "phala/gpt-oss-20b",
		ProviderConfig:    domain.ProviderConfig{ProviderName: "phala"},
		MaxOutputTokens:   1024,
	}

	built := buildProviderRequest(req, model)
	if got := built.Body["reasoning_effort"]; got != "low" {
		t.Fatalf("reasoning_effort = %v, want low", got)
	}
}

func TestBuildProviderRequest_DeveloperRolePolicy(t *testing.T) {
	req := domain.GenerateRequest{
		PublicModelID: "test/developer-role",
		Input:         []domain.InputItem{message("developer", "follow instructions")},
	}

	compatible := buildProviderRequest(req, domain.PublicModel{
		UpstreamModelName: "compatible-model",
		ProviderConfig:    domain.ProviderConfig{ProviderName: "phala"},
	})
	messages := compatible.Body["messages"].([]map[string]any)
	if messages[0]["role"] != "system" {
		t.Fatalf("compatible role = %v, want system", messages[0]["role"])
	}
	if !containsString(compatible.Transforms, "developer_role=system") {
		t.Fatalf("transforms = %v, want developer_role=system", compatible.Transforms)
	}

	openaiBuilt := buildProviderRequest(req, domain.PublicModel{
		UpstreamModelName: "gpt-5",
		ProviderConfig:    domain.ProviderConfig{ProviderName: "openai"},
	})
	messages = openaiBuilt.Body["messages"].([]map[string]any)
	if messages[0]["role"] != "developer" {
		t.Fatalf("openai role = %v, want developer", messages[0]["role"])
	}
}

func TestBuildProviderRequest_OmittedTokenLimitStaysOmitted(t *testing.T) {
	req := domain.GenerateRequest{
		PublicModelID: "openai/test",
		Input:         []domain.InputItem{message("user", "hi")},
	}
	model := domain.PublicModel{
		UpstreamModelName: "test-model",
		ProviderConfig:    domain.ProviderConfig{ProviderName: "openai"},
		MaxOutputTokens:   128,
	}

	built := buildProviderRequest(req, model)
	if _, ok := built.Body["max_completion_tokens"]; ok {
		t.Fatal("max_completion_tokens must stay omitted when the client omits a token limit")
	}
	if _, ok := built.Body["max_tokens"]; ok {
		t.Fatal("max_tokens must stay omitted when the client omits a token limit")
	}
}

func TestBuildProviderRequest_NovitaPolicy(t *testing.T) {
	maxTokens := 32
	req := domain.GenerateRequest{
		PublicModelID:   "moonshotai/kimi-k2.6",
		Input:           []domain.InputItem{message("user", "hi")},
		MaxOutputTokens: &maxTokens,
	}
	model := novitaModel("http://example.test")

	built := buildProviderRequest(req, model)
	if built.Policy != "novita" {
		t.Fatalf("policy = %q, want novita", built.Policy)
	}
	if got := built.Body["max_tokens"]; got != 32 {
		t.Fatalf("max_tokens = %v, want 32", got)
	}
	if _, ok := built.Body["max_completion_tokens"]; ok {
		t.Fatal("Novita policy must not send max_completion_tokens")
	}
	if !containsString(built.Transforms, "token_limit_field=max_tokens") {
		t.Fatalf("transforms = %v, want token_limit_field=max_tokens", built.Transforms)
	}
}

func TestBuildProviderRequest_DeepSeekPolicy(t *testing.T) {
	maxTokens := 32
	req := domain.GenerateRequest{
		PublicModelID:   "deepseek/deepseek-v4-pro",
		Input:           []domain.InputItem{message("user", "hi")},
		MaxOutputTokens: &maxTokens,
	}
	model := deepseekModel("http://example.test")

	built := buildProviderRequest(req, model)
	if built.Policy != "deepseek" {
		t.Fatalf("policy = %q, want deepseek", built.Policy)
	}
	if got := built.Body["max_tokens"]; got != 32 {
		t.Fatalf("max_tokens = %v, want 32", got)
	}
	if _, ok := built.Body["max_completion_tokens"]; ok {
		t.Fatal("DeepSeek policy must not send max_completion_tokens")
	}
	if !containsString(built.Transforms, "token_limit_field=max_tokens") {
		t.Fatalf("transforms = %v, want token_limit_field=max_tokens", built.Transforms)
	}
}

func TestBuildProviderRequest_DeepSeekAssistantToolContentNull(t *testing.T) {
	req := domain.GenerateRequest{
		PublicModelID: "deepseek/deepseek-v4-pro",
		Input: []domain.InputItem{
			{
				Type:      domain.InputItemTypeMessage,
				Role:      stringPtr("assistant"),
				ToolCalls: []domain.ToolCall{{ID: "call_1", Name: "noop", ArgumentsJSON: "{}"}},
			},
		},
	}

	built := buildProviderRequest(req, deepseekModel("http://example.test"))
	messages := built.Body["messages"].([]map[string]any)
	if _, ok := messages[0]["content"]; !ok {
		t.Fatal("DeepSeek assistant tool-call message must include content key")
	}
	if messages[0]["content"] != nil {
		t.Fatalf("content = %v, want nil", messages[0]["content"])
	}
	if !containsString(built.Transforms, "assistant_tool_content=null") {
		t.Fatalf("transforms = %v, want assistant_tool_content=null", built.Transforms)
	}
	if got, ok := messages[0]["reasoning_content"].(string); !ok || got != "" {
		t.Fatalf("reasoning_content = %#v, want empty string", messages[0]["reasoning_content"])
	}
	if !containsString(built.Transforms, "assistant_tool_reasoning_content=empty") {
		t.Fatalf("transforms = %v, want assistant_tool_reasoning_content=empty", built.Transforms)
	}
}

func TestBuildProviderRequest_DeepSeekPreservesAssistantReasoningContent(t *testing.T) {
	reasoning := "used the search result"
	req := domain.GenerateRequest{
		PublicModelID: "deepseek/deepseek-v4-pro",
		Input: []domain.InputItem{
			{
				Type:             domain.InputItemTypeMessage,
				Role:             stringPtr("assistant"),
				ReasoningContent: &reasoning,
				ToolCalls:        []domain.ToolCall{{ID: "call_1", Name: "noop", ArgumentsJSON: "{}"}},
			},
		},
	}

	built := buildProviderRequest(req, deepseekModel("http://example.test"))
	messages := built.Body["messages"].([]map[string]any)
	if got := messages[0]["reasoning_content"]; got != reasoning {
		t.Fatalf("reasoning_content = %#v, want %q", got, reasoning)
	}
	if containsString(built.Transforms, "assistant_tool_reasoning_content=empty") {
		t.Fatalf("transforms = %v, must not synthesize empty reasoning when client supplied it", built.Transforms)
	}
}

func TestBuildProviderRequest_NovitaAssistantToolContentNull(t *testing.T) {
	req := domain.GenerateRequest{
		PublicModelID: "moonshotai/kimi-k2.6",
		Input: []domain.InputItem{
			{
				Type:      domain.InputItemTypeMessage,
				Role:      stringPtr("assistant"),
				ToolCalls: []domain.ToolCall{{ID: "call_1", Name: "noop", ArgumentsJSON: "{}"}},
			},
		},
	}

	built := buildProviderRequest(req, novitaModel("http://example.test"))
	messages := built.Body["messages"].([]map[string]any)
	if _, ok := messages[0]["content"]; !ok {
		t.Fatal("Novita assistant tool-call message must include content key")
	}
	if messages[0]["content"] != nil {
		t.Fatalf("content = %v, want nil", messages[0]["content"])
	}
	if !containsString(built.Transforms, "assistant_tool_content=null") {
		t.Fatalf("transforms = %v, want assistant_tool_content=null", built.Transforms)
	}
	if _, ok := messages[0]["reasoning_content"]; ok {
		t.Fatal("Novita assistant tool-call message must not include DeepSeek reasoning_content compatibility field")
	}
}

func TestBuildNovitaRetryRequest_GuardedDowngrade(t *testing.T) {
	body := map[string]any{
		"model":               "moonshotai/kimi-k2.6",
		"parallel_tool_calls": false,
		"store":               true,
		"service_tier":        "auto",
		"user":                "user-1",
		"tool_choice":         "auto",
	}

	retry := buildNovitaRetryRequest(body)
	if !retry.CanRetry {
		t.Fatal("expected retry to be allowed")
	}
	for _, field := range []string{"parallel_tool_calls", "store", "service_tier", "user", "tool_choice"} {
		if _, ok := retry.Body[field]; ok {
			t.Fatalf("retry body still contains %s", field)
		}
	}
	for _, omitted := range []string{"parallel_tool_calls", "store", "service_tier", "user", "tool_choice=auto"} {
		if !containsString(retry.Omitted, omitted) {
			t.Fatalf("omitted = %v, missing %s", retry.Omitted, omitted)
		}
	}
}

func TestBuildNovitaRetryRequest_ToolChoiceNoneRemovesTools(t *testing.T) {
	body := map[string]any{
		"model":       "moonshotai/kimi-k2.6",
		"tool_choice": "none",
		"tools":       []map[string]any{{"type": "function"}},
	}

	retry := buildNovitaRetryRequest(body)
	if !retry.CanRetry {
		t.Fatal("expected retry to be allowed")
	}
	if _, ok := retry.Body["tool_choice"]; ok {
		t.Fatal("retry body still contains tool_choice")
	}
	if _, ok := retry.Body["tools"]; ok {
		t.Fatal("retry body still contains tools")
	}
}

func TestBuildNovitaRetryRequest_NamedToolChoiceIsNotDowngraded(t *testing.T) {
	body := map[string]any{
		"model": "moonshotai/kimi-k2.6",
		"tool_choice": map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "noop"},
		},
	}

	retry := buildNovitaRetryRequest(body)
	if retry.CanRetry {
		t.Fatalf("named tool_choice must not be downgraded, omitted = %v", retry.Omitted)
	}
}

func TestAdapterGenerate_NovitaRetriesSafeDowngrade(t *testing.T) {
	t.Setenv("NOVITA_TEST_KEY", "test-key")
	var attempts int
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		if attempts <= 3 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"invalid request error trace_id: testtrace","type":"invalid_request_error"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"cmpl-1","created":123,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	maxTokens := 8
	parallel := false
	req := domain.GenerateRequest{
		PublicModelID:     "moonshotai/kimi-k2.6",
		Input:             []domain.InputItem{message("user", "hi")},
		MaxOutputTokens:   &maxTokens,
		ParallelToolCalls: &parallel,
		Store:             boolPtr(true),
		ServiceTier:       stringPtr("auto"),
		User:              stringPtr("user-1"),
		ToolChoice:        &domain.ToolChoice{Mode: domain.ToolChoiceAuto},
		Tools:             []domain.ToolDefinition{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
	}

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, err := adapter.Generate(context.Background(), req, novitaModel(server.URL))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if attempts != 4 {
		t.Fatalf("attempts = %d, want 4", attempts)
	}
	for _, field := range []string{"parallel_tool_calls", "store", "service_tier", "user", "tool_choice"} {
		if _, ok := bodies[1][field]; !ok {
			t.Fatalf("same-body retry should still contain %s", field)
		}
	}
	for _, field := range []string{"parallel_tool_calls", "store", "service_tier", "user", "tool_choice"} {
		if _, ok := bodies[2][field]; !ok {
			t.Fatalf("second same-body retry should still contain %s", field)
		}
	}
	for _, field := range []string{"parallel_tool_calls", "store", "service_tier", "user", "tool_choice"} {
		if _, ok := bodies[3][field]; ok {
			t.Fatalf("downgrade retry body still contains %s", field)
		}
	}
	if _, ok := bodies[3]["tools"]; !ok {
		t.Fatal("tool_choice=auto retry should keep tools")
	}
}

func TestAdapterGenerate_NovitaNamedToolChoiceFailsClearlyAfterSameBodyRetry(t *testing.T) {
	t.Setenv("NOVITA_TEST_KEY", "test-key")
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"invalid request error trace_id: namedtrace","type":"invalid_request_error"}`))
	}))
	defer server.Close()

	maxTokens := 8
	req := domain.GenerateRequest{
		PublicModelID:   "moonshotai/kimi-k2.6",
		Input:           []domain.InputItem{message("user", "hi")},
		MaxOutputTokens: &maxTokens,
		ToolChoice: &domain.ToolChoice{
			Mode:         domain.ToolChoiceFunction,
			FunctionName: stringPtr("noop"),
		},
		Tools: []domain.ToolDefinition{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
	}

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, err := adapter.Generate(context.Background(), req, novitaModel(server.URL))
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if !strings.Contains(err.Error(), "tool_choice") {
		t.Fatalf("error = %q, want clear tool_choice context", err.Error())
	}
}

func TestAdapterGenerate_NovitaSafeRetryStillReportsNamedToolChoiceIncompatibility(t *testing.T) {
	t.Setenv("NOVITA_TEST_KEY", "test-key")
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"invalid request error trace_id: namedtrace","type":"invalid_request_error"}`))
	}))
	defer server.Close()

	maxTokens := 8
	parallel := false
	req := domain.GenerateRequest{
		PublicModelID:     "moonshotai/kimi-k2.6",
		Input:             []domain.InputItem{message("user", "hi")},
		MaxOutputTokens:   &maxTokens,
		ParallelToolCalls: &parallel,
		ToolChoice: &domain.ToolChoice{
			Mode:         domain.ToolChoiceFunction,
			FunctionName: stringPtr("noop"),
		},
		Tools: []domain.ToolDefinition{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
	}

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, err := adapter.Generate(context.Background(), req, novitaModel(server.URL))
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 4 {
		t.Fatalf("attempts = %d, want 4", attempts)
	}
	if !strings.Contains(err.Error(), "tool_choice") {
		t.Fatalf("error = %q, want clear tool_choice context", err.Error())
	}
}

func TestAdapterGenerate_NovitaFailedSameBodyRetryIncludesProviderParams(t *testing.T) {
	t.Setenv("NOVITA_TEST_KEY", "test-key")
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"invalid request error trace_id: tooltrace","type":"invalid_request_error"}`))
	}))
	defer server.Close()

	maxTokens := 8
	req := domain.GenerateRequest{
		PublicModelID:   "moonshotai/kimi-k2.6",
		Input:           []domain.InputItem{message("user", "hi")},
		MaxOutputTokens: &maxTokens,
		Tools:           []domain.ToolDefinition{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
	}

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, err := adapter.Generate(context.Background(), req, novitaModel(server.URL))
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}

	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) {
		t.Fatalf("error = %T, want *domain.GatewayError", err)
	}
	if got := gwErr.Metadata["retry_outcome"]; got != "same_body_failed_no_safe_downgrade" {
		t.Fatalf("retry_outcome = %v, want same_body_failed_no_safe_downgrade", got)
	}
	params, ok := gwErr.Metadata["provider_params"].(map[string]any)
	if !ok {
		t.Fatalf("provider_params = %T, want map[string]any", gwErr.Metadata["provider_params"])
	}
	if got := params["tool_count"]; got != 1 {
		t.Fatalf("provider_params.tool_count = %v, want 1", got)
	}
	if _, ok := params["messages"]; !ok {
		t.Fatalf("provider_params = %v, want redacted message summary", params)
	}
}

func TestAdapterGenerate_NovitaServerOverloadRetriesSameBody(t *testing.T) {
	t.Setenv("NOVITA_TEST_KEY", "test-key")
	var attempts int
	var firstBody map[string]any
	var secondBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if attempts == 1 {
			firstBody = body
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"server overload, please try again later trace_id: overloadtrace","type":"server_overload"}`))
			return
		}
		secondBody = body
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"cmpl-1","created":123,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	maxTokens := 8
	parallel := false
	req := domain.GenerateRequest{
		PublicModelID:     "moonshotai/kimi-k2.6",
		Input:             []domain.InputItem{message("user", "hi")},
		MaxOutputTokens:   &maxTokens,
		ParallelToolCalls: &parallel,
		Tools:             []domain.ToolDefinition{{Name: "noop", Parameters: map[string]any{"type": "object"}}},
	}

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, err := adapter.Generate(context.Background(), req, novitaModel(server.URL))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if got, want := secondBody["parallel_tool_calls"], firstBody["parallel_tool_calls"]; got != want {
		t.Fatalf("same-body retry changed parallel_tool_calls: got %v, want %v", got, want)
	}
	if _, ok := secondBody["tools"]; !ok {
		t.Fatal("same-body retry should keep tools")
	}
}

func TestSummarizeProviderBodyRedactsPromptTextUserAndToolSchema(t *testing.T) {
	body := map[string]any{
		"model": "moonshotai/kimi-k2.6",
		"user":  "raw-user-id",
		"messages": []map[string]any{
			{"role": "user", "content": "secret prompt text"},
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "lookup",
					"description": "secret tool description",
					"parameters":  map[string]any{"description": "secret schema"},
				},
			},
		},
	}

	summary := summarizeProviderBody(body)
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	got := string(raw)
	for _, secret := range []string{"secret prompt text", "raw-user-id", "secret tool description", "secret schema"} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"user":"[redacted]"`) {
		t.Fatalf("summary = %s, want redacted user marker", got)
	}
	if !strings.Contains(got, `"content_chars":18`) {
		t.Fatalf("summary = %s, want content length", got)
	}
	if !strings.Contains(got, `"tool_names":["lookup"]`) {
		t.Fatalf("summary = %s, want tool name only", got)
	}
}

func novitaModel(baseURL string) domain.PublicModel {
	return domain.PublicModel{
		PublicModelID:     "moonshotai/kimi-k2.6",
		UpstreamModelName: "moonshotai/kimi-k2.6",
		MaxOutputTokens:   262144,
		ProviderConfig: domain.ProviderConfig{
			ProviderName:    "novita",
			BaseURL:         baseURL,
			APIKeySecretRef: "NOVITA_TEST_KEY",
		},
	}
}

func deepseekModel(baseURL string) domain.PublicModel {
	return domain.PublicModel{
		PublicModelID:     "deepseek/deepseek-v4-pro",
		UpstreamModelName: "deepseek-v4-pro",
		MaxOutputTokens:   8192,
		ProviderConfig: domain.ProviderConfig{
			ProviderName:    "deepseek",
			BaseURL:         baseURL,
			APIKeySecretRef: "DEEPSEEK_TEST_KEY",
		},
	}
}

func message(role, content string) domain.InputItem {
	return domain.InputItem{
		Type:    domain.InputItemTypeMessage,
		Role:    stringPtr(role),
		Content: stringPtr(content),
	}
}

func stringPtr(v string) *string {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
