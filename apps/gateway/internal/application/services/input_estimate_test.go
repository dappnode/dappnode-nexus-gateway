package services

import (
	"context"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func TestEstimateInputTokens(t *testing.T) {
	inlineImage := `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + strings.Repeat("A", 200_000) + `"}}`
	linkedImage := `{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}`
	text := `{"type":"text","text":"what is this?"}`

	cases := []struct {
		name string
		body string
		// extra is what the estimate adds to the body's bytes; ok=false means
		// no bound.
		extra func(body string) int64
		ok    bool
	}{
		{
			name:  "string content counts every byte",
			body:  `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello"}]}`,
			extra: func(string) int64 { return 0 },
			ok:    true,
		},
		{
			name:  "text parts count every byte",
			body:  `{"model":"m","messages":[{"role":"user","content":[` + text + `,{"type":"input_text","text":"x"}]}]}`,
			extra: func(string) int64 { return 0 },
			ok:    true,
		},
		{
			name:  "null content and tool calls",
			body:  `{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"1","content":"ok"}]}`,
			extra: func(string) int64 { return 0 },
			ok:    true,
		},
		{
			name:  "inline image counts a fixed allowance instead of its bytes",
			body:  `{"model":"m","messages":[{"role":"user","content":[` + text + `,` + inlineImage + `]}]}`,
			extra: func(string) int64 { return imageTokens - int64(len(inlineImage)) },
			ok:    true,
		},
		{
			name:  "linked image counts the allowance too",
			body:  `{"model":"m","messages":[{"role":"user","content":[` + linkedImage + `]}]}`,
			extra: func(string) int64 { return imageTokens - int64(len(linkedImage)) },
			ok:    true,
		},
		{
			name:  "every image counts",
			body:  `{"model":"m","messages":[{"role":"user","content":[` + linkedImage + `]},{"role":"tool","tool_call_id":"1","content":[` + linkedImage + `]}]}`,
			extra: func(string) int64 { return 2 * (imageTokens - int64(len(linkedImage))) },
			ok:    true,
		},
		{
			name: "audio has no bound",
			body: `{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]}`,
		},
		{
			name: "video has no bound",
			body: `{"model":"m","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}]}]}`,
		},
		{
			name: "files have no bound",
			body: `{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"f-1"}}]}]}`,
		},
		{
			name: "an unknown part type has no bound",
			body: `{"model":"m","messages":[{"role":"user","content":[{"type":"hologram"}]}]}`,
		},
		{
			name: "a part without a type has no bound",
			body: `{"model":"m","messages":[{"role":"user","content":[{"text":"hi"}]}]}`,
		},
		{
			name: "a part that is not an object has no bound",
			body: `{"model":"m","messages":[{"role":"user","content":["hi"]}]}`,
		},
		{
			name: "invalid JSON has no bound",
			body: `{"model":"m","messages":[`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := estimateInputTokens(domain.EndpointChatCompletions, []byte(tc.body))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			want := int64(len(tc.body)) + promptOverheadTokens + tc.extra(tc.body)
			if got != want {
				t.Fatalf("estimate = %d, want %d", got, want)
			}
		})
	}
}

func TestEstimateInputTokens_InlineImageDoesNotHoldItsBytes(t *testing.T) {
	image := `{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + strings.Repeat("A", 2_000_000) + `"}}`
	body := `{"model":"m","messages":[{"role":"user","content":[` + image + `]}]}`
	got, ok := estimateInputTokens(domain.EndpointChatCompletions, []byte(body))
	if !ok || got > imageTokens+promptOverheadTokens+100 {
		t.Fatalf("estimate = %d (ok=%v), want about one image allowance, not 2M bytes", got, ok)
	}
}

func TestEstimateInputTokens_SystemOneHasNoBound(t *testing.T) {
	if _, ok := estimateInputTokens(domain.EndpointSystemOne, []byte(`{"model":"m","input":"hi"}`)); ok {
		t.Fatal("systemone builds its own prompt, so it must not get an estimate")
	}
}

func TestProxy_SendsThePromptEstimateWithTheHold(t *testing.T) {
	meter := &stubUsageMeter{}
	svc := newDirectModelGenerateService(meter, &stubProvider{})
	body := `{"model":"model","messages":[{"role":"user","content":"hello"}]}`

	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(body), domain.GenerateRequest{PublicModelID: "model"}, "sk-test"); err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	estimate := meter.lastReserveReq.EstimatedInputTokens
	if want := int64(len(body)) + promptOverheadTokens; estimate == nil || *estimate != want {
		t.Fatalf("estimate sent with the hold = %v, want %d", estimate, want)
	}
}

func TestProxy_SendsNoEstimateWhenThePromptHasNoBound(t *testing.T) {
	meter := &stubUsageMeter{}
	svc := newDirectModelGenerateService(meter, &stubProvider{})
	body := `{"model":"model","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]}`

	if _, err := svc.Proxy(context.Background(), domain.EndpointChatCompletions, []byte(body), domain.GenerateRequest{PublicModelID: "model"}, "sk-test"); err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if meter.lastReserveReq.EstimatedInputTokens != nil {
		t.Fatalf("estimate = %d, want none so the hold covers the context window", *meter.lastReserveReq.EstimatedInputTokens)
	}
}
