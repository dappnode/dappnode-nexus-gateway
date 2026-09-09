package handlers_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/handlers"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/openai"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/registry"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type streamUsageReproResult struct {
	result domain.GenerateResult
	err    error
}

type streamUsageReproMeter struct {
	mockUsageMeter
	recorded chan streamUsageReproResult
}

func (m *streamUsageReproMeter) RecordSuccess(_ context.Context, _ string, _ domain.AuthContext, _ string, _ domain.GenerateRequest, result domain.GenerateResult, _ domain.PublicModel, _ int64) error {
	m.recorded <- streamUsageReproResult{result: result}
	return nil
}

func (m *streamUsageReproMeter) RecordFailure(_ context.Context, _ *string, _ *domain.AuthContext, _ string, _ *domain.GenerateRequest, _ *domain.PublicModel, err error, _ *domain.Usage, _ int64) error {
	m.recorded <- streamUsageReproResult{err: err}
	return nil
}

// End-to-end smoke test through real HTTP, the production OpenAI adapter,
// handler, and usage tracker. Only auth, catalog, and metering are faked.
// No database, credentials, or paid inference is involved.
func TestStreamingUsageRetention(t *testing.T) {
	const usage = `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}`
	const toolDelta = `{"tool_calls":[{"index":0,"id":"call-local","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`
	t.Setenv("NEXUS_STREAM_REPRO_PROVIDER_KEY", "local-test-only")

	for _, tc := range []struct {
		name         string
		placement    string
		closeOnDone  bool
		closeOnDelta bool
		wantUsage    bool
	}{
		{name: "trailing_usage", placement: "trailing", wantUsage: true},
		{name: "client_cancels_before_finish", placement: "unfinished", closeOnDelta: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			upstreamCanceled := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Stream        bool `json:"stream"`
					StreamOptions struct {
						IncludeUsage bool `json:"include_usage"`
					} `json:"stream_options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Stream || !request.StreamOptions.IncludeUsage {
					t.Error("gateway did not request streaming with include_usage=true")
					http.Error(w, "invalid streaming request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(delta, finish, tokenUsage string) {
					fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-local-repro\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}],\"usage\":%s}\n\n", delta, finish, tokenUsage)
					w.(http.Flusher).Flush()
				}
				emit(toolDelta, "null", "null")
				if tc.placement == "unfinished" {
					select {
					case <-r.Context().Done():
						close(upstreamCanceled)
					case <-ctx.Done():
					}
					return
				}
				emit(`{}`, `"tool_calls"`, "null")
				// Delay trailing usage so the handler's drain loop has work to do.
				delay := time.NewTimer(25 * time.Millisecond)
				defer delay.Stop()
				select {
				case <-delay.C:
					fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-local-repro\",\"choices\":[],\"usage\":%s}\n\n", usage)
				case <-r.Context().Done():
					close(upstreamCanceled)
					return
				case <-ctx.Done():
					return
				}
				io.WriteString(w, "data: [DONE]\n\n")
				w.(http.Flusher).Flush()
			}))
			defer upstream.Close()

			model := domain.PublicModel{
				PublicModelID: "local-repro", ProviderModelID: "local-repro", UpstreamModelName: "local-repro", Active: true,
				SupportsChatCompletions: true, SupportsChatCompletionsStream: true, SupportsTools: true,
				ProviderConfig: domain.ProviderConfig{
					ProviderName: "novita", BaseURL: upstream.URL,
					APIKeySecretRef: "NEXUS_STREAM_REPRO_PROVIDER_KEY",
				},
			}
			logger := &mockLogger{}
			providers := registry.NewRegistry()
			providers.Register("novita", openai.NewAdapter(5*time.Second))
			meter := &streamUsageReproMeter{recorded: make(chan streamUsageReproResult, 2)}
			generate := services.NewGenerateService(&mockAuthService{}, &mockModelCatalog{model: model}, nil, providers, meter, nil, logger)
			handler := handlers.NewChatCompletionsHandler(services.NewChatCompletionsService(generate, logger), logger)
			gateway := httptest.NewServer(http.HandlerFunc(handler.Handle))
			defer gateway.Close()

			request, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/v1/chat/completions", strings.NewReader(`{"model":"local-repro","stream":true,"messages":[{"role":"user","content":"Call lookup"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}]}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer local-test-only")
			request.Header.Set("Content-Type", "application/json")
			response, err := gateway.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("gateway status=%d body=%s", response.StatusCode, body)
			}
			scanner := bufio.NewScanner(response.Body)
			sawDone := false
			sawFinish := false
			for scanner.Scan() {
				if tc.closeOnDelta && strings.Contains(scanner.Text(), `"tool_calls"`) {
					response.Body.Close()
					break
				}
				if strings.Contains(scanner.Text(), `"finish_reason":"tool_calls"`) {
					sawFinish = true
				}
				if scanner.Text() != "data: [DONE]" {
					continue
				}
				sawDone = true
				if tc.closeOnDone {
					response.Body.Close()
					break
				}
			}
			if !tc.closeOnDelta && (!sawDone || !sawFinish) {
				t.Fatalf("downstream sawDone=%t read error=%v", sawDone, scanner.Err())
			}
			select {
			case recorded := <-meter.recorded:
				if tc.closeOnDelta {
					if recorded.err == nil {
						t.Fatal("client cancellation before finish was recorded as success")
					}
					select {
					case <-upstreamCanceled:
					case <-ctx.Done():
						t.Fatal("upstream continued generating after early client cancellation")
					}
					return
				}
				if recorded.err != nil {
					t.Fatalf("recorded failure: %v", recorded.err)
				}
				result := recorded.result
				if result.ID != "chatcmpl-local-repro" || result.FinishReason == nil || *result.FinishReason != "tool_calls" {
					t.Fatalf("unexpected completion: %+v", result)
				}
				if (result.Usage != nil) != tc.wantUsage {
					t.Fatalf("usage=%+v, want usage present=%t", result.Usage, tc.wantUsage)
				}
				if result.Usage != nil && (result.Usage.PromptTokens != 100 || result.Usage.CompletionTokens != 20) {
					t.Fatalf("incorrect usage: %+v", result.Usage)
				}
				select {
				case <-upstreamCanceled:
					t.Fatal("upstream canceled before trailing usage was sent")
				default:
				}
				t.Logf("success=true finish_reason=tool_calls provider_request_id=%s usage=%+v", result.ID, result.Usage)
			case <-ctx.Done():
				t.Fatal("metering did not complete before timeout")
			}
		})
	}
}
