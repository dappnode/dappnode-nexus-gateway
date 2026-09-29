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

func TestAdapterComplete_NovitaRetriesSafeDowngrade(t *testing.T) {
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

	raw := []byte(`{"model":"moonshotai/kimi-k2.6","messages":[{"role":"user","content":"hi"}],"max_tokens":8,"parallel_tool_calls":false,"store":true,"service_tier":"auto","user":"user-1","tool_choice":"auto","tools":[{"type":"function","function":{"name":"noop","parameters":{"type":"object"}}}]}`)

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, _, err := adapter.Complete(context.Background(), raw, novitaModel(server.URL))
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
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

func TestAdapterComplete_NovitaNamedToolChoiceFailsClearlyAfterSameBodyRetry(t *testing.T) {
	t.Setenv("NOVITA_TEST_KEY", "test-key")
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"invalid request error trace_id: namedtrace","type":"invalid_request_error"}`))
	}))
	defer server.Close()

	raw := []byte(`{"model":"moonshotai/kimi-k2.6","messages":[{"role":"user","content":"hi"}],"max_tokens":8,"tool_choice":{"type":"function","function":{"name":"noop"}},"tools":[{"type":"function","function":{"name":"noop","parameters":{"type":"object"}}}]}`)

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, _, err := adapter.Complete(context.Background(), raw, novitaModel(server.URL))
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

func TestAdapterComplete_NovitaSafeRetryStillReportsNamedToolChoiceIncompatibility(t *testing.T) {
	t.Setenv("NOVITA_TEST_KEY", "test-key")
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"invalid request error trace_id: namedtrace","type":"invalid_request_error"}`))
	}))
	defer server.Close()

	raw := []byte(`{"model":"moonshotai/kimi-k2.6","messages":[{"role":"user","content":"hi"}],"max_tokens":8,"parallel_tool_calls":false,"tool_choice":{"type":"function","function":{"name":"noop"}},"tools":[{"type":"function","function":{"name":"noop","parameters":{"type":"object"}}}]}`)

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, _, err := adapter.Complete(context.Background(), raw, novitaModel(server.URL))
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

func TestAdapterComplete_NovitaFailedSameBodyRetryIncludesProviderParams(t *testing.T) {
	t.Setenv("NOVITA_TEST_KEY", "test-key")
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"invalid request error trace_id: tooltrace","type":"invalid_request_error"}`))
	}))
	defer server.Close()

	raw := []byte(`{"model":"moonshotai/kimi-k2.6","messages":[{"role":"user","content":"hi"}],"max_tokens":8,"tools":[{"type":"function","function":{"name":"noop","parameters":{"type":"object"}}}]}`)

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, _, err := adapter.Complete(context.Background(), raw, novitaModel(server.URL))
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

func TestAdapterComplete_NovitaServerOverloadRetriesSameBody(t *testing.T) {
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

	raw := []byte(`{"model":"moonshotai/kimi-k2.6","messages":[{"role":"user","content":"hi"}],"max_tokens":8,"parallel_tool_calls":false,"tools":[{"type":"function","function":{"name":"noop","parameters":{"type":"object"}}}]}`)

	adapter := &Adapter{
		client: &Client{httpClient: server.Client(), responseTimeout: time.Second},
	}
	_, _, err := adapter.Complete(context.Background(), raw, novitaModel(server.URL))
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
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
	if !strings.Contains(got, `"tool_count":1`) {
		t.Fatalf("summary = %s, want tool count only", got)
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

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestProviderSummaryOmitsFreeformFields(t *testing.T) {
	const secret = "PRIVATE-PROMPT-CANARY"
	body := map[string]any{
		secret: secret, "model": secret, "stop": []string{secret}, "temperature": secret,
		"tool_choice":     map[string]any{"function": map[string]any{"name": secret}},
		"response_format": map[string]any{"type": secret},
		"stream_options":  map[string]any{"include_usage": true, secret: secret},
		"messages":        []map[string]any{{"role": secret, "content": secret, "reasoning_content": secret, "tool_calls": []map[string]any{{"function": map[string]any{"name": secret, "arguments": secret}}}}},
		"tools":           []map[string]any{{"function": map[string]any{"name": secret}}},
	}
	encoded, err := json.Marshal(summarizeProviderBody(body))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("private data leaked: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"include_usage":true`) {
		t.Fatal("missing usage request metadata")
	}
}
