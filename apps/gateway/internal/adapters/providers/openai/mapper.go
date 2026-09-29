package openai

type providerPolicy struct {
	name                        string
	useMaxTokens                bool
	requireToolReasoningContent bool
	forwardDeveloperRole        bool
}

type builtProviderRequest struct {
	Body       map[string]any
	Policy     string
	Transforms []string
}

type retryBuildResult struct {
	Body        map[string]any
	RetryReason string
	CanRetry    bool
}

func policyForProvider(providerName string) providerPolicy {
	switch providerName {
	case "deepseek":
		// Verified against DeepSeek's API (2026-09-29): it silently ignores
		// max_completion_tokens, and in thinking mode rejects assistant
		// tool-call turns sent back without reasoning_content.
		return providerPolicy{
			name:                        "deepseek",
			useMaxTokens:                true,
			requireToolReasoningContent: true,
		}
	default:
		// Every provider but OpenAI gets developer messages as system ones:
		// DeepSeek and Kimi K3 reject the developer role, and GLM 5.3 on
		// Novita ignores it (verified 2026-09-29).
		return providerPolicy{
			name:                 "openai-compatible",
			forwardDeveloperRole: providerName == "openai",
		}
	}
}
