package openai

import (
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type providerPolicy struct {
	name                             string
	useMaxTokens                     bool
	explicitNullAssistantToolContent bool
	requireToolReasoningContent      bool
	forwardDeveloperRole             bool
}

type builtProviderRequest struct {
	Body       map[string]any
	Policy     string
	Transforms []string
	Omitted    []string
}

type retryBuildResult struct {
	Body        map[string]any
	Omitted     []string
	RetryReason string
	CanRetry    bool
}

func policyForProvider(providerName string) providerPolicy {
	switch providerName {
	case "deepseek":
		// DeepSeek exposes an OpenAI-compatible chat API at /chat/completions,
		// but currently documents the legacy `max_tokens` field and nullable
		// assistant tool-call content.
		return providerPolicy{
			name:                             "deepseek",
			useMaxTokens:                     true,
			explicitNullAssistantToolContent: true,
			requireToolReasoningContent:      true,
		}
	case "novita":
		// Novita exposes an OpenAI-compatible API, but its public Chat
		// Completions docs currently document `max_tokens` rather than
		// OpenAI's newer `max_completion_tokens`, and require a `content`
		// field that may be null for assistant tool-call messages. Keep these
		// as Novita-only wire-shape translations; do not leak them into the
		// public OpenAI-compatible gateway API.
		return providerPolicy{
			name:                             "novita",
			useMaxTokens:                     true,
			explicitNullAssistantToolContent: true,
		}
	default:
		return providerPolicy{
			name:                 "openai-compatible",
			forwardDeveloperRole: providerName == "openai",
		}
	}
}

func buildNovitaRetryRequest(body map[string]any) retryBuildResult {
	retryBody := cloneBody(body)
	omitted := make([]string, 0, 6)

	for _, field := range []string{"parallel_tool_calls", "store", "service_tier", "user"} {
		if _, ok := retryBody[field]; ok {
			delete(retryBody, field)
			omitted = append(omitted, field)
		}
	}

	if toolChoice, ok := retryBody["tool_choice"]; ok {
		switch v := toolChoice.(type) {
		case string:
			switch v {
			case domain.ToolChoiceAuto:
				delete(retryBody, "tool_choice")
				omitted = append(omitted, "tool_choice=auto")
			case domain.ToolChoiceNone:
				delete(retryBody, "tool_choice")
				delete(retryBody, "tools")
				omitted = append(omitted, "tool_choice=none", "tools")
			}
		}
	}

	return retryBuildResult{
		Body:        retryBody,
		Omitted:     omitted,
		RetryReason: "novita_invalid_request_guarded_downgrade",
		CanRetry:    len(omitted) > 0,
	}
}

func cloneBody(body map[string]any) map[string]any {
	clone := make(map[string]any, len(body))
	for k, v := range body {
		clone[k] = v
	}
	return clone
}
