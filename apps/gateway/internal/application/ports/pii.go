package ports

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// PIIAnalyzeOptions controls detection for a single gateway request.
type PIIAnalyzeOptions struct {
	// Language is an ISO-639-1 code such as "en"; an empty string means
	// "use the implementation's default".
	Language string
	// Mode is the Nexus PII masking level selected on the API key.
	Mode string
}

// PIIFilter detects personally-identifiable information in arbitrary text so
// the gateway can mask it before forwarding requests to upstream providers and
// un-mask matching tokens on the way back. Implementations MUST be safe for
// concurrent use.
type PIIFilter interface {
	// Analyze returns the PII spans found in `text`. Implementations should
	// return entities with byte offsets into `text` (not character offsets)
	// so that callers can substitute them with strings.Builder safely.
	//
	Analyze(ctx context.Context, text string, opts PIIAnalyzeOptions) ([]domain.PIIEntity, error)

	// Enabled reports whether the filter is operational. When false, the
	// gateway will skip both detection and the un-masking pass.
	Enabled() bool
}
