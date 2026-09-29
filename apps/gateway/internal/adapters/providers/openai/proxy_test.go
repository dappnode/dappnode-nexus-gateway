package openai

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func prepared(t *testing.T, raw, provider string, stream bool) map[string]any {
	t.Helper()
	body, _, err := PrepareProxyBody([]byte(raw), domain.PublicModel{
		UpstreamModelName: "upstream-name", MaxOutputTokens: 8000,
		ProviderConfig: domain.ProviderConfig{ProviderName: provider},
	}, stream)
	if err != nil {
		t.Fatal(err)
	}
	// Compare as JSON the provider will receive.
	b, _ := json.Marshal(body)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Everything the client sends reaches the provider, including what the
// gateway doesn't model: images, provider extensions, reasoning options.
func TestPrepareProxyBody_ForwardsTheClientRequest(t *testing.T) {
	raw := `{"model":"public/x","messages":[{"role":"user","content":[{"type":"text","text":"What is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"}}]},
		{"role":"assistant","content":"Let me look.","reasoning_content":"thinking","tool_calls":[{"id":"a","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"a","content":[{"type":"text","text":"file"}]}],
		"tools":[{"type":"function","function":{"name":"read","description":"Read","parameters":{"type":"object","properties":{}},"strict":false}}],
		"temperature":0.7,"seed":9007199254740993,"top_k":40,"chat_template_kwargs":{"enable_thinking":false},"reasoning":{"effort":"high"},"response_format":{"type":"json_object"},
		"n":1,"logprobs":true,"provider_options":{"x":1}}`
	got := prepared(t, raw, "phala", false)
	want := decode(t, raw)
	want["model"] = "upstream-name"
	delete(want, "provider_options")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request changed:\n got %v\nwant %v", got, want)
	}
	// Large integers are forwarded exactly.
	body, _, _ := PrepareProxyBody([]byte(raw), domain.PublicModel{UpstreamModelName: "m"}, false)
	if b, _ := json.Marshal(body["seed"]); string(b) != "9007199254740993" {
		t.Fatalf("seed = %s", b)
	}
}

func TestPrepareProxyBody_Edits(t *testing.T) {
	t.Run("streams always report usage", func(t *testing.T) {
		got := prepared(t, `{"messages":[],"stream":true,"stream_options":{"include_usage":false,"continuous_usage_stats":true}}`, "novita", true)
		options := got["stream_options"].(map[string]any)
		if got["stream"] != true || options["include_usage"] != true || options["continuous_usage_stats"] != true {
			t.Fatalf("stream_options %v", options)
		}
	})
	t.Run("output limit clamped and named per provider", func(t *testing.T) {
		for provider, field := range map[string]string{"deepseek": "max_tokens", "novita": "max_completion_tokens", "tinfoil": "max_completion_tokens", "phala": "max_completion_tokens"} {
			got := prepared(t, `{"messages":[],"max_completion_tokens":50000}`, provider, false)
			if got[field] != float64(8000) || len(got) != 3 {
				t.Fatalf("%s: %v", provider, got)
			}
		}
		// Other providers get the client's own field.
		if got := prepared(t, `{"messages":[],"max_tokens":100}`, "tinfoil", false); got["max_tokens"] != float64(100) || len(got) != 3 {
			t.Fatalf("field renamed: %v", got)
		}
	})
	t.Run("provider compatibility for valid OpenAI requests", func(t *testing.T) {
		raw := `{"messages":[{"role":"developer","content":"be brief"},{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`
		deepseek := prepared(t, raw, "deepseek", false)["messages"].([]any)
		if deepseek[0].(map[string]any)["role"] != "system" {
			t.Fatal("developer role not mapped")
		}
		assistant := deepseek[1].(map[string]any)
		if _, ok := assistant["content"]; ok || assistant["reasoning_content"] != "" {
			t.Fatalf("deepseek assistant %v", assistant)
		}
		other := prepared(t, raw, "tinfoil", false)["messages"].([]any)[1].(map[string]any)
		if _, ok := other["reasoning_content"]; ok {
			t.Fatal("reasoning_content added for a provider that doesn't need it")
		}
	})
}

// The provider policies, for requests the old translating path also covered.
func TestPrepareProxyBody_ProviderPolicies(t *testing.T) {
	// No limit from the client means no limit sent.
	if got := prepared(t, `{"messages":[]}`, "novita", false); len(got) != 2 {
		t.Fatalf("limit invented: %v", got)
	}
	// DeepSeek keeps the reasoning the client sent back.
	raw := `{"messages":[{"role":"assistant","content":"x","reasoning_content":"because","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`
	msg := prepared(t, raw, "deepseek", false)["messages"].([]any)[0].(map[string]any)
	if msg["reasoning_content"] != "because" || msg["content"] != "x" {
		t.Fatalf("assistant message changed: %v", msg)
	}
	// OpenAI itself gets the developer role.
	if role := prepared(t, `{"messages":[{"role":"developer","content":"x"}]}`, "openai", false)["messages"].([]any)[0].(map[string]any)["role"]; role != "developer" {
		t.Fatalf("role = %v", role)
	}
}
