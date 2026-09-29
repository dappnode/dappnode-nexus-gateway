package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/observability/metrics"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// gatewayOnlyFields are request fields for the gateway itself; providers
// never see them.
var gatewayOnlyFields = []string{"provider_options"}

// PrepareProxyBody turns the client's request body into the provider's. The
// gateway is a proxy: every field the client sent is forwarded unchanged,
// including ones the gateway doesn't know (images, reasoning options,
// provider extensions). The only edits are:
//
//   - model: the provider's name for the public model;
//   - stream_options.include_usage: on for streams, so usage can be metered;
//   - the output-token limit: clamped to the model's maximum, and sent as
//     max_tokens to the providers that only document that name (Novita,
//     DeepSeek);
//   - provider compatibility for valid OpenAI requests the provider would
//     otherwise reject: developer role as system, content:null on assistant
//     tool-call messages, and reasoning_content on DeepSeek tool-call turns;
//   - gateway-only fields are removed.
func PrepareProxyBody(raw []byte, model domain.PublicModel, stream bool) (map[string]any, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // Numbers are forwarded exactly as sent.
	var body map[string]any
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, nil, domain.ErrInvalidField("invalid JSON body")
	}
	policy := policyForProvider(model.ProviderConfig.ProviderName)
	var transforms []string

	body["model"] = model.UpstreamModelName
	for _, field := range gatewayOnlyFields {
		delete(body, field)
	}
	if stream {
		body["stream"] = true
		options, _ := body["stream_options"].(map[string]any)
		if options == nil {
			options = map[string]any{}
		}
		options["include_usage"] = true
		body["stream_options"] = options
	} else {
		delete(body, "stream_options") // Only valid on streams.
	}

	if limit, field, ok := tokenLimit(body); ok {
		if model.MaxOutputTokens > 0 && limit > int64(model.MaxOutputTokens) {
			limit = int64(model.MaxOutputTokens)
			transforms = append(transforms, "token_limit=clamped")
		}
		delete(body, "max_tokens")
		delete(body, "max_completion_tokens")
		target := field // Keep the client's field, unless the provider only knows max_tokens.
		if policy.useMaxTokens {
			target = "max_tokens"
		}
		if target != field {
			transforms = append(transforms, "token_limit_field="+target)
		}
		body[target] = limit
	}

	messages := asMaps(body["messages"])
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role == "developer" && !policy.forwardDeveloperRole {
			msg["role"] = "system"
			transforms = append(transforms, "developer_role=system")
		}
		if role != "assistant" {
			continue
		}
		toolCalls := asMaps(msg["tool_calls"])
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		if _, has := msg["content"]; !has && len(toolCalls) > 0 && policy.explicitNullAssistantToolContent {
			msg["content"] = nil
			transforms = append(transforms, "assistant_tool_content=null")
		}
		if _, has := msg["reasoning_content"]; !has && len(toolCalls) > 0 && policy.requireToolReasoningContent {
			msg["reasoning_content"] = ""
			transforms = append(transforms, "assistant_tool_reasoning_content=empty")
		}
	}
	if messages != nil {
		body["messages"] = messages
	}
	if tools := asMaps(body["tools"]); tools != nil {
		body["tools"] = tools
	}
	return body, dedupe(transforms), nil
}

// tokenLimit reads the client's output-token limit and which field held it.
func tokenLimit(body map[string]any) (int64, string, bool) {
	for _, field := range []string{"max_completion_tokens", "max_tokens"} {
		if n, ok := body[field].(json.Number); ok {
			if v, err := n.Int64(); err == nil {
				return v, field, true
			}
		}
	}
	return 0, "", false
}

// asMaps views a JSON array of objects as maps (shared with the body, so
// edits apply in place). It returns nil for anything else.
func asMaps(v any) []map[string]any {
	switch list := v.(type) {
	case []map[string]any:
		return list
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				return nil
			}
			out = append(out, m)
		}
		return out
	}
	return nil
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	out := list[:0]
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Stream forwards a streaming request, with the provider's retry policy,
// and returns the provider's SSE body untouched.
func (a *Adapter) Stream(ctx context.Context, raw []byte, model domain.PublicModel) (ports.ProviderStream, error) {
	body, transforms, err := PrepareProxyBody(raw, model, true)
	if err != nil {
		return ports.ProviderStream{}, err
	}
	resp, err := a.proxy(ctx, body, transforms, model, func(ctx context.Context, apiKey string, body map[string]any) (*http.Response, error) {
		return a.client.DoStream(ctx, model.ProviderConfig.BaseURL, apiKey, body)
	})
	if err != nil {
		return ports.ProviderStream{}, err
	}
	return ports.ProviderStream{Body: resp.Body}, nil
}

// Complete forwards a non-streaming request and returns the provider's JSON.
func (a *Adapter) Complete(ctx context.Context, raw []byte, model domain.PublicModel) ([]byte, *domain.TinfoilTransportProof, error) {
	body, transforms, err := PrepareProxyBody(raw, model, false)
	if err != nil {
		return nil, nil, err
	}
	var out []byte
	_, err = a.proxy(ctx, body, transforms, model, func(ctx context.Context, apiKey string, body map[string]any) (*http.Response, error) {
		var err error
		out, err = a.client.Do(ctx, model.ProviderConfig.BaseURL, apiKey, body)
		if err != nil {
			return nil, err
		}
		return &http.Response{Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})
	return out, nil, err
}

// proxy sends body with the provider's retry policy (Novita's transient
// rejections), and maps provider errors to gateway errors.
func (a *Adapter) proxy(ctx context.Context, body map[string]any, transforms []string, model domain.PublicModel, send func(context.Context, string, map[string]any) (*http.Response, error)) (*http.Response, error) {
	apiKey := resolveAPIKey(model.ProviderConfig.APIKeySecretRef)
	if apiKey == "" {
		return nil, missingProviderCredentialError(model.ProviderConfig.ProviderName)
	}
	built := builtProviderRequest{Body: body, Policy: policyForProvider(model.ProviderConfig.ProviderName).name, Transforms: transforms}
	active := built
	var retryReason string
	invalidTraceRetries, overloadRetries := 0, 0
	downgradeRetried := false
	for attempt := 1; ; attempt++ {
		a.logProviderRequest(ctx, model, active, attempt, retryReason)
		resp, err := send(ctx, apiKey, active.Body)
		if err == nil {
			return resp, nil
		}
		if retry := maybeBuildNovitaSameBodyRetry(model, err, active.Body); retry.CanRetry &&
			canSpendSameBodyRetry(retry.RetryReason, &invalidTraceRetries, &overloadRetries) {
			retryReason = retry.RetryReason
			metrics.ProviderRetries.WithLabelValues(model.ProviderConfig.ProviderName, retryReason).Inc()
			if !sleepBeforeProviderRetry(ctx, retryReason) {
				return nil, withProviderPolicyMeta(mapProviderError(context.Cause(ctx), model.ProviderConfig.ProviderName), active, attempt, retryReason)
			}
			continue
		}
		if !downgradeRetried {
			if retry := maybeBuildNovitaDowngradeRetry(model, err, active.Body); retry.CanRetry {
				downgradeRetried = true
				retryReason = retry.RetryReason
				metrics.ProviderRetries.WithLabelValues(model.ProviderConfig.ProviderName, retryReason).Inc()
				active = builtProviderRequest{Body: retry.Body, Policy: built.Policy, Transforms: transforms, Omitted: retry.Omitted}
				if !sleepBeforeProviderRetry(ctx, retryReason) {
					return nil, withProviderPolicyMeta(mapProviderError(context.Cause(ctx), model.ProviderConfig.ProviderName), active, attempt, retryReason)
				}
				continue
			}
		}
		return nil, withProviderPolicyMeta(mapProviderErrorWithCompatibilityContext(err, model, active.Body), active, attempt, retryReason)
	}
}
