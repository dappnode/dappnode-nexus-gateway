package presidio

import (
	"context"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// NoopFilter is a PIIFilter that never detects anything. It is used when the
// PII filter is disabled by configuration so that callers can keep the same
// code path without nil checks.
type NoopFilter struct{}

// NewNoopFilter returns a no-op filter.
func NewNoopFilter() *NoopFilter { return &NoopFilter{} }

// Enabled always returns false.
func (NoopFilter) Enabled() bool { return false }

// Analyze always returns no entities and no error.
func (NoopFilter) Analyze(context.Context, string, ports.PIIAnalyzeOptions) ([]domain.PIIEntity, error) {
	return nil, nil
}
