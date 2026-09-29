package mapper

import (
	"errors"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func read(t *testing.T, body string) (domain.GenerateRequest, error) {
	t.Helper()
	return ChatCompletionRequestToDomain([]byte(body))
}

func TestChatCompletionRequestToDomain_ReadsTheSummary(t *testing.T) {
	req, err := read(t, `{"model":"m","stream":true,"max_completion_tokens":50,"parallel_tool_calls":false,
		"response_format":{"type":"json_object"},"temperature":0.2,"unknown_extension":{"x":1},
		"messages":[{"role":"developer","content":"be brief"},
			{"role":"user","content":[{"type":"text","text":"What is "},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}},{"type":"text","text":"this?"}]},
			{"role":"assistant","content":null,"reasoning_content":"r","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"a","content":"42"}],
		"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if req.PublicModelID != "m" || !req.Stream || *req.MaxOutputTokens != 50 || *req.ParallelToolCalls || !req.StructuredOutput {
		t.Fatalf("summary %+v", req)
	}
	if len(req.Input) != 4 || *req.Input[0].Role != "developer" || *req.Input[1].Content != "What is this?" || req.Input[2].Content != nil || *req.Input[3].Content != "42" {
		t.Fatalf("router view %+v", req.Input)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "f" {
		t.Fatalf("tools %+v", req.Tools)
	}
	if req, _ := read(t, `{"model":"m","max_tokens":7,"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"text"}}`); *req.MaxOutputTokens != 7 || req.StructuredOutput {
		t.Fatalf("max_tokens / text format: %+v", req)
	}
}

func TestChatCompletionRequestToDomain_Rejects(t *testing.T) {
	msg := `"messages":[{"role":"user","content":"hi"}]`
	for body, want := range map[string]string{
		`{not json`:                   "invalid JSON body",
		`{` + msg + `}`:               "model is required",
		`{"model":"m","messages":[]}`: "messages is required",
		`{"model":"m",` + msg + `,"max_tokens":1,"max_completion_tokens":1}`:         "both max_tokens",
		`{"model":"m",` + msg + `,"n":2}`:                                            "'n' only supports",
		`{"model":"m",` + msg + `,"functions":[]}`:                                   "'functions' is not supported",
		`{"model":"m","messages":[{"role":"tool","content":"x"}]}`:                   "requires tool_call_id",
		`{"model":"m","messages":[{"role":"system","content":"x","tool_calls":[]}]}`: "must not contain tool_calls",
		`{"model":"m","messages":[{"role":"user"}]}`:                                 "content is required",
		`{"model":"m","messages":[{"role":"robot","content":"x"}]}`:                  "unsupported role",
		`{"model":"m",` + msg + `,"tools":[{"type":"web","function":{"name":"f"}}]}`: "unsupported tool type",
		`{"model":"m",` + msg + `,"tools":[{"type":"function","function":{}}]}`:      "name is required",
		`{"model":"m",` + msg + `,"tool_choice":"sometimes"}`:                        "invalid tool_choice",
		`{"model":"m",` + msg + `,"tool_choice":{"type":"function","function":{}}}`:  "function name is required",
	} {
		_, err := read(t, body)
		var gwErr *domain.GatewayError
		if !errors.As(err, &gwErr) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", body, err, want)
		}
	}
}
