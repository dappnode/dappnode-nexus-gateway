package services

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// fakePIIFilter returns configured entities per exact text and counts calls.
type fakePIIFilter struct {
	enabled  bool
	err      error
	calls    int
	lastOpts ports.PIIAnalyzeOptions
	byText   map[string][]domain.PIIEntity
}

func (f *fakePIIFilter) Enabled() bool { return f.enabled }

func (f *fakePIIFilter) Analyze(_ context.Context, text string, opts ports.PIIAnalyzeOptions) ([]domain.PIIEntity, error) {
	f.calls++
	f.lastOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.byText[text], nil
}

type captureLogger struct {
	stubLogger
	warnings []string
}

func (l *captureLogger) Warn(msg string, _ ...any) { l.warnings = append(l.warnings, msg) }

func piiService(filter ports.PIIFilter, mode string, provider *stubProvider) (*GenerateService, *resultMeter) {
	meter := &resultMeter{}
	svc := proxyService(meter, "", map[string]ports.Provider{"primary": provider}, false)
	svc.pii = filter
	svc.auth = &stubAuthService{authCtx: domain.AuthContext{Account: domain.Account{ID: "acc1", Status: domain.AccountStatusActive}, APIKey: domain.APIKey{ID: "key1", Active: true, PIIMode: mode}}}
	return svc, meter
}

func entity(kind string, text, in string) domain.PIIEntity {
	start := strings.Index(in, text)
	return domain.PIIEntity{Type: kind, Start: start, End: start + len(text), Score: 0.99}
}

// The LLM-bound text of the request is masked and the response restored;
// everything else is forwarded as sent.
func TestPII_MasksRequestAndRestoresResponse(t *testing.T) {
	prompt := "Email Jane at jane@example.com"
	reasoning := "I should contact Jane"
	filter := &fakePIIFilter{enabled: true, byText: map[string][]domain.PIIEntity{
		prompt:             {entity("PERSON", "Jane", prompt), entity("EMAIL", "jane@example.com", prompt)},
		reasoning:          {entity("PERSON", "Jane", reasoning)},
		"jane@example.com": {entity("EMAIL", "jane@example.com", "jane@example.com")},
		"Call Jane":        {entity("PERSON", "Jane", "Call Jane")},
		"Alice":            {entity("PERSON", "Alice", "Alice")},
		"Look at Alice":    {entity("PERSON", "Alice", "Look at Alice")},
	}}
	provider := &stubProvider{json: `{"id":"r1","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"Message [PERSON_1] at [EMAIL_1].","tool_calls":[{"id":"c","type":"function","function":{"name":"send","arguments":"{\"email\":\"[EMAIL_1]\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1},"system_fingerprint":"fp"}`}
	svc, _ := piiService(filter, domain.APIKeyPIIModeBalanced, provider)
	raw := `{"model":"m","user":"jane@example.com","messages":[
		{"role":"user","content":"` + prompt + `"},
		{"role":"user","content":[{"type":"text","text":"Look at Alice"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},
		{"role":"assistant","reasoning_content":"` + reasoning + `","tool_calls":[{"id":"c1","type":"function","function":{"name":"send","arguments":"{\"email\":\"jane@example.com\",\"note\":\"Call Jane\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"{\"customer\":{\"name\":\"Alice\"},\"paid\":false,\"id\":9007199254740993}"}],
		"tools":[{"type":"function","function":{"name":"send","description":"Send email to Jane","parameters":{"type":"object"}}}],"temperature":0.2}`
	call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(raw), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key")
	if err != nil {
		t.Fatal(err)
	}
	sent := provider.lastRaw
	for _, leaked := range []string{"jane@example.com", "Contact Jane", "Call Jane", `"name":"Alice"`, "Look at Alice", "Email Jane"} {
		if strings.Contains(sent, leaked) {
			t.Fatalf("upstream body leaks %q: %s", leaked, sent)
		}
	}
	for _, kept := range []string{"[EMAIL_1]", "[PERSON_1]", `"Send email to Jane"`, `data:image/png;base64,AAAA`, `9007199254740993`, `"temperature":0.2`, `\"paid\":false`} {
		if !strings.Contains(sent, kept) {
			t.Fatalf("upstream body missing %s: %s", kept, sent)
		}
	}
	var resp map[string]any
	if err := json.Unmarshal(call.Body, &resp); err != nil {
		t.Fatal(err)
	}
	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "Message Jane at jane@example.com." {
		t.Fatalf("content = %v", msg["content"])
	}
	args := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"]
	if args != `{"email":"jane@example.com"}` || resp["system_fingerprint"] != "fp" || resp["model"] != "zai-org/glm-5.3-flash" {
		t.Fatalf("response %s", call.Body)
	}
	if filter.lastOpts.Mode != domain.APIKeyPIIModeBalanced {
		t.Fatalf("mode = %q", filter.lastOpts.Mode)
	}
}

func TestPII_InvalidJSONToolArgumentsMaskedAsText(t *testing.T) {
	args := `email=jane@example.com note=Call Jane`
	filter := &fakePIIFilter{enabled: true, byText: map[string][]domain.PIIEntity{
		args: {entity("EMAIL", "jane@example.com", args), entity("PERSON", "Jane", args)},
	}}
	provider := &stubProvider{}
	svc, _ := piiService(filter, domain.APIKeyPIIModeHigh, provider)
	raw := `{"messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"` + args + `"}}]}]}`
	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(raw), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.lastRaw, `"arguments":"email=[EMAIL_1] note=Call [PERSON_1]"`) {
		t.Fatalf("sent %s", provider.lastRaw)
	}
}

func TestPII_BypassAndFailureModes(t *testing.T) {
	raw := `{"messages":[{"role":"user","content":"hi Jane"}],"seed":9007199254740993}`
	detect := map[string][]domain.PIIEntity{"hi Jane": {entity("PERSON", "Jane", "hi Jane")}}
	for name, tc := range map[string]struct {
		filter   *fakePIIFilter
		mode     string
		failOpen bool
		wantErr  bool
		wantSent string // "" means sent unchanged
	}{
		"key without masking": {filter: &fakePIIFilter{enabled: true, byText: detect}, mode: domain.APIKeyPIIModeOff},
		"filter off":          {filter: &fakePIIFilter{enabled: false, byText: detect}, mode: domain.APIKeyPIIModeHigh},
		"nothing found":       {filter: &fakePIIFilter{enabled: true}, mode: domain.APIKeyPIIModeHigh},
		"filter fails closed": {filter: &fakePIIFilter{enabled: true, err: io.ErrUnexpectedEOF}, mode: domain.APIKeyPIIModeHigh, wantErr: true},
		"filter fails open":   {filter: &fakePIIFilter{enabled: true, err: io.ErrUnexpectedEOF}, mode: domain.APIKeyPIIModeHigh, failOpen: true},
		"masking":             {filter: &fakePIIFilter{enabled: true, byText: detect}, mode: domain.APIKeyPIIModeLow, wantSent: `hi [PERSON_1]`},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &stubProvider{}
			svc, meter := piiService(tc.filter, tc.mode, provider)
			svc.piiFailOpen = tc.failOpen
			_, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(raw), domain.GenerateRequest{PublicModelID: "zai-org/glm-5.3-flash"}, "key")
			if tc.wantErr {
				if err == nil || provider.calls != 0 || meter.reserveCalls != 0 {
					t.Fatalf("err=%v calls=%d reserve=%d", err, provider.calls, meter.reserveCalls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantSent == "" && provider.lastRaw != raw {
				t.Fatalf("body changed: %s", provider.lastRaw)
			}
			if tc.wantSent != "" && (!strings.Contains(provider.lastRaw, tc.wantSent) || !strings.Contains(provider.lastRaw, "9007199254740993")) {
				t.Fatalf("sent %s", provider.lastRaw)
			}
		})
	}
}

// streamed collects what a client reassembles from a proxied stream.
type streamed struct {
	content, reasoning string
	args               map[int]string
	finish             string
	raw                string
}

func runPIIStream(t *testing.T, sse string, logger ports.Logger) streamed {
	t.Helper()
	filter := &fakePIIFilter{enabled: true, byText: map[string][]domain.PIIEntity{
		"Email jane@example.com and John": {entity("EMAIL", "jane@example.com", "Email jane@example.com and John"), entity("PERSON", "John", "Email jane@example.com and John")},
	}}
	provider := &stubProvider{sse: sse}
	svc, _ := piiService(filter, domain.APIKeyPIIModeBalanced, provider)
	if logger != nil {
		svc.logger = logger
	}
	call, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(`{"stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"Email jane@example.com and John"}]}`), streamRequest(), "key")
	if err != nil {
		t.Fatal(err)
	}
	got, err := relayAll(t, call.Stream)
	if err != nil {
		t.Fatal(err)
	}
	call.Stream.Close()
	out := streamed{args: map[int]string{}, raw: got}
	for _, line := range strings.Split(got, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int `json:"index"`
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatalf("invalid event %q: %v", data, err)
		}
		for _, ch := range c.Choices {
			out.content += ch.Delta.Content
			out.reasoning += ch.Delta.ReasoningContent
			for _, tc := range ch.Delta.ToolCalls {
				out.args[tc.Index] += tc.Function.Arguments
			}
			if ch.FinishReason != nil {
				out.finish = *ch.FinishReason
			}
		}
	}
	return out
}

func ev(delta string, finish string) string {
	f := "null"
	if finish != "" {
		f = `"` + finish + `"`
	}
	return `data: {"id":"s1","model":"upstream","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + f + `}]}` + "\n\n"
}

func TestPII_StreamRestoresPlaceholders(t *testing.T) {
	sse := ev(`{"role":"assistant"}`, "") +
		ev(`{"reasoning_content":"Write to [PER"}`, "") +
		ev(`{"reasoning_content":"SON_1]"}`, "") +
		ev(`{"content":"Hi [PERSON_1], see [GHOST_5] and [EMA"}`, "") +
		ev(`{"content":"IL_1]"}`, "") +
		ev(`{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"send","arguments":"{\"to\":\"EMA"}}]}`, "") +
		ev(`{"tool_calls":[{"index":0,"function":{"arguments":"IL_1\"}"}}]}`, "") +
		ev(`{"content":" bye [PER"}`, "stop") +
		`data: {"id":"s1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"
	got := runPIIStream(t, sse, nil)
	if got.content != "Hi John, see [GHOST_5] and jane@example.com bye [PER" || got.reasoning != "Write to John" || got.finish != "stop" {
		t.Fatalf("content %q reasoning %q finish %q", got.content, got.reasoning, got.finish)
	}
	// A placeholder the model wrote without brackets, split across events.
	if got.args[0] != `{"to":"jane@example.com"}` {
		t.Fatalf("args %q", got.args[0])
	}
	// The event with no text passes byte for byte.
	if !strings.Contains(got.raw, `data: {"id":"s1","model":"zai-org/glm-5.3-flash","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`) {
		t.Fatalf("role event changed: %s", got.raw)
	}
}

func TestPII_StreamReleasesHeldTextWhenTheProviderNeverFinishes(t *testing.T) {
	got := runPIIStream(t, ev(`{"content":"Hi [PERSON_1] and [EMA"}`, "")+ev(`{"content":"IL_1] then [PER"}`, "")+"data: [DONE]\n\n", nil)
	if got.content != "Hi John and jane@example.com then [PER" {
		t.Fatalf("content %q", got.content)
	}
	if strings.Count(got.raw, "[DONE]") != 1 || strings.Index(got.raw, "then [PER") > strings.Index(got.raw, "[DONE]") {
		t.Fatalf("held text not released before [DONE]: %s", got.raw)
	}
}

func TestPII_StreamToolArgumentsSplitAcrossEvents(t *testing.T) {
	sse := ev(`{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"send","arguments":"{\"to\":\"[EMA"}},{"index":1,"id":"b","type":"function","function":{"name":"send","arguments":"{\"to\":\"[PERSON_1]\"}"}}]}`, "") +
		ev(`{"tool_calls":[{"index":0,"function":{"arguments":"IL_1]\"}"}}]}`, "tool_calls") + "data: [DONE]\n\n"
	got := runPIIStream(t, sse, nil)
	if got.args[0] != `{"to":"jane@example.com"}` || got.args[1] != `{"to":"John"}` || got.finish != "tool_calls" {
		t.Fatalf("args %v finish %q", got.args, got.finish)
	}
}

func TestPII_LogsPlaceholdersTheModelChanged(t *testing.T) {
	logger := &captureLogger{}
	runPIIStream(t, ev(`{"content":"Hi [PERSON_7]"}`, "stop")+"data: [DONE]\n\n", logger)
	found := false
	for _, w := range logger.warnings {
		found = found || w == "pii restoration unresolved tokens"
	}
	if !found {
		t.Fatalf("warnings %v", logger.warnings)
	}
}

func TestSanitizeErrorWithPIIMapping_MasksKnownOriginalsInMessageAndMetadata(t *testing.T) {
	m := domain.NewPIIMapping()
	m.Token("EMAIL", "jane@example.com")
	err := domain.ErrProviderError(502, "provider echoed jane@example.com").WithMeta(
		"upstream_error", `bad request for jane@example.com`,
		"nested", map[string]any{"body": "jane@example.com"},
	)
	sanitized, ok := sanitizeErrorWithPIIMapping(err, m).(*domain.GatewayError)
	if !ok {
		t.Fatalf("expected GatewayError")
	}
	if strings.Contains(sanitized.Message, "jane@example.com") || sanitized.Metadata["upstream_error"] != `bad request for [EMAIL_1]` ||
		sanitized.Metadata["nested"].(map[string]any)["body"] != "[EMAIL_1]" {
		t.Fatalf("not sanitized: %+v", sanitized)
	}
}
