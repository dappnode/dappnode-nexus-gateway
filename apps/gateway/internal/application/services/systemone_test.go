package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/mapper"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/providers/typesafe"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

const evaluationRequest = `{"model":"public-jev","state":{"email":"jane@example.com","id":9007199254740993},"questions":{"urgent":{"type":"noul","instructions":"Urgent?","criteria":{"true":"Yes","false":"No"}},"team":{"type":"choice","instructions":{"question":"Which team?"},"criteria":{"sales":null,"support":["Help"]}},"rating":{"type":"score","instructions":["Rate it"],"criteria":["jane@example.com","Other"]}}}`
const evaluationResponse = `{"model":"jev-1.13.0","answers":{"urgent":{"type":"noul","noul":0.95},"team":{"type":"choice","choice":"support","probabilities":{"support":1,"sales":0},"confidence":1},"rating":{"type":"score","score":0,"legend":{"0":"[EMAIL_1]","1":"Other"},"probabilities":{"0":1,"1":0},"confidence":1}},"usage":{"input_tokens":296,"output_tokens":20}}`

func TestSystemOne(t *testing.T) {
	t.Setenv("TEST_TYPESAFE_KEY", "upstream-secret")
	status := http.StatusOK
	response := evaluationResponse
	upstreamCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.URL.Path != "/v1/systemone" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "jane@example.com") || !strings.Contains(string(raw), "9007199254740993") || !strings.Contains(string(raw), `"model":"jev-latest"`) {
			t.Errorf("request masking/model/precision broken: %s", raw)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	model := domain.PublicModel{PublicModelID: "public-jev", ProviderModelID: "pm", UpstreamModelName: "jev-latest", ProviderConfig: domain.ProviderConfig{ProviderName: "typesafe", BaseURL: server.URL + "/v1/", APIKeySecretRef: "TEST_TYPESAFE_KEY"}, MaxContextWindow: 4096, MaxOutputTokens: 256}
	meter := &resultMeter{}
	auth := &stubAuthService{authCtx: domain.AuthContext{Account: domain.Account{ID: "acc"}, APIKey: domain.APIKey{ID: "key", PIIMode: domain.APIKeyPIIModeBalanced}}}
	filter := &fakePIIFilter{enabled: true, byText: map[string][]domain.PIIEntity{"jane@example.com": {entity("EMAIL", "jane@example.com", "jane@example.com")}}}
	catalog := &stubModelCatalog{model: model}
	svc := NewGenerateService(auth, catalog, nil, &stubProviderRegistry{providers: map[string]ports.Provider{"typesafe": typesafe.NewAdapter(time.Second)}}, meter, filter, stubLogger{})
	req, err := mapper.SystemOneRequestToDomain([]byte(evaluationRequest))
	if err != nil {
		t.Fatal(err)
	}
	run := func() (*ProxyCall, error) {
		return svc.Proxy(context.Background(), domain.EndpointSystemOne, []byte(evaluationRequest), req, "nexus-key")
	}
	call, err := run()
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal(call.Body, &got)
	_ = json.Unmarshal([]byte(strings.ReplaceAll(strings.ReplaceAll(evaluationResponse, "jev-1.13.0", "public-jev"), "[EMAIL_1]", "jane@example.com")), &want)
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("answers changed: %s", call.Body)
	}
	if meter.reserveCalls != 1 || meter.successCalls != 1 || meter.result.Usage.PromptTokens != 296 || meter.result.Usage.CompletionTokens != 20 || meter.result.Usage.TotalTokens != 316 {
		t.Fatalf("incorrect billing: %+v", meter)
	}

	for _, code := range []int{422, 429, 529, 401} {
		status = code
		response = `{"detail":"upstream failure"}`
		_, err = run()
		expected := code
		if code == 401 {
			expected = 502
		}
		ge, ok := err.(*domain.GatewayError)
		if !ok || ge.HTTPStatus != expected {
			t.Fatalf("upstream %d mapped to %v", code, err)
		}
	}
	if meter.failureCalls != 4 || meter.successCalls != 1 {
		t.Fatal("upstream failures were charged")
	}
	status = http.StatusOK
	for _, malformed := range []string{`null`, `{"model":"jev","answers":{"a":{}},"usage":{}}`, `{"model":"jev","answers":{"a":{}},"usage":{"input_tokens":-1,"output_tokens":2}}`} {
		response = malformed
		if _, err := run(); err == nil {
			t.Fatalf("accepted unbillable response: %s", malformed)
		}
	}
	response = evaluationResponse
	before := upstreamCalls
	auth.err = domain.ErrInvalidAPIKey("invalid")
	if _, err := run(); err == nil {
		t.Fatal("accepted invalid API key")
	}
	auth.err = nil
	meter.reserveErr = domain.ErrInternal("no credit")
	if _, err := run(); err == nil {
		t.Fatal("called provider without reservation")
	}
	meter.reserveErr = nil
	filter.err = fmt.Errorf("PII unavailable")
	if _, err := run(); err == nil {
		t.Fatal("PII failed open")
	}
	filter.err = nil
	catalog.model.ProviderConfig.ProviderName = "openai"
	if _, err := run(); err == nil {
		t.Fatal("accepted chat model")
	}
	catalog.model = model
	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(evaluationRequest), req, "key"); err == nil {
		t.Fatal("accepted typesafe on chat endpoint")
	}
	if upstreamCalls != before {
		t.Fatal("rejected requests reached provider")
	}
	// A configured chat fallback must not receive structured evaluation requests.
	fallback := &stubProvider{}
	svc.registry = &stubProviderRegistry{providers: map[string]ports.Provider{"typesafe": typesafe.NewAdapter(time.Second), "openai": fallback}}
	catalog.model.Fallback = &domain.ProviderTarget{ProviderConfig: domain.ProviderConfig{ProviderName: "openai"}}
	status = 529
	if _, err := run(); err == nil || fallback.calls != 0 {
		t.Fatal("incompatible fallback used")
	} else if ge, ok := err.(*domain.GatewayError); !ok || ge.HTTPStatus != 529 {
		t.Fatalf("incompatible fallback hid upstream status: %v", err)
	}
}

func TestSystemOneValidation(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, evaluationRequest + `{}`, strings.Replace(evaluationRequest, `"model":"public-jev"`, `"model":""`, 1),
		strings.Replace(evaluationRequest, `"model":"public-jev"`, `"model":"public-jev","stream":true`, 1),
		`{"model":"m","state":null,"questions":{}}`, `{"model":"m","state":42,"questions":{}}`,
		`{"model":"m","state":"s","questions":{"q":{"type":"other","instructions":"i"}}}`,
		`{"model":"m","state":[],"questions":{"q":{"type":"noul","instructions":true}}}`,
		`{"model":"m","state":{},"questions":{"q":{"type":"score","instructions":"i","criteria":["one"]}}}`,
		`{"model":"m","state":"s","questions":{"q":{"type":"choice","instructions":"i","criteria":{}}}}`,
	} {
		if _, err := mapper.SystemOneRequestToDomain([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, state := range []string{`"text"`, `{}`, `[]`} {
		raw := `{"model":"m","state":` + state + `,"questions":{"q":{"type":"noul","instructions":"i"}}}`
		if _, err := mapper.SystemOneRequestToDomain([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
}
