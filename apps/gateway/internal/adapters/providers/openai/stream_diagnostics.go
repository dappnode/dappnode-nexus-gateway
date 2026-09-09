package openai

import (
	"context"
	"errors"
	"io"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/observability/logfields"
)

// Diagnostics contain metadata only: never SSE bodies, content, or arguments.
type streamDiagnostics struct {
	ctx                                                            context.Context
	logger                                                         ports.Logger
	fields                                                         []any
	providerID                                                     string
	finishReason                                                   *string
	chunks, malformed, unsupported, usageChunks, mappedUsageChunks int
	usage                                                          *domain.Usage
	logged                                                         bool
}

func (s *Stream) WithDiagnostics(ctx context.Context, logger ports.Logger, model domain.PublicModel, attempt int) *Stream {
	if logger != nil {
		s.diagnostics = &streamDiagnostics{ctx: ctx, logger: logger, fields: []any{
			"request_id", middleware.GetRequestID(ctx), "provider", model.ProviderConfig.ProviderName,
			"provider_model", model.UpstreamModelName, "model", model.PublicModelID, "attempt", attempt,
		}}
	}
	return s
}

func (d *streamDiagnostics) observe(chunk chatCompletionChunk, events []domain.StreamEvent) {
	if chunk.ID != "" {
		d.providerID = chunk.ID
	}
	hasTools := false
	var finish *string
	for _, choice := range chunk.Choices {
		if choice.FinishReason != nil {
			finish = choice.FinishReason
			d.finishReason = finish
		}
		hasTools = hasTools || len(choice.Delta.ToolCalls) > 0
	}
	mapped := false
	for _, event := range events {
		mapped = mapped || event.Usage != nil
	}
	if chunk.Usage != nil {
		d.usageChunks++
		d.usage = chunkUsageToDomain(chunk.Usage)
	}
	if mapped {
		d.mappedUsageChunks++
	}
	if chunk.Usage != nil || finish != nil {
		fields := append([]any{}, d.fields...)
		fields = append(fields, "provider_request_id", d.providerID, "chunk_index", d.chunks,
			"finish_reason", logfields.FinishReason(finish), "has_tool_calls", hasTools, "choices_count", len(chunk.Choices),
			"usage_received", chunk.Usage != nil, "usage_forwarded", mapped, "usage", chunkUsageToDomain(chunk.Usage))
		d.logger.Debug("provider stream chunk metadata", fields...)
	}
}

func (d *streamDiagnostics) end(reason string, err error) {
	if d == nil || d.logged {
		return
	}
	d.logged = true
	if errors.Is(err, context.Canceled) || errors.Is(d.ctx.Err(), context.Canceled) {
		if reason == "read_error" {
			reason = "canceled"
		}
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(d.ctx.Err(), context.DeadlineExceeded) {
		if reason == "read_error" {
			reason = "deadline_exceeded"
		}
	}
	fields := append([]any{}, d.fields...)
	fields = append(fields, "provider_request_id", d.providerID, "upstream_end", reason,
		"upstream_done_seen", reason == "done_marker", "chunks_received", d.chunks,
		"malformed_chunks", d.malformed, "unsupported_data_lines", d.unsupported,
		"usage_chunks", d.usageChunks, "mapped_usage_chunks", d.mappedUsageChunks,
		"usage_received", d.usage != nil, "usage", d.usage, "finish_reason", logfields.FinishReason(d.finishReason),
		"context_canceled", errors.Is(d.ctx.Err(), context.Canceled))
	if d.usage == nil || d.malformed > 0 || d.unsupported > 0 || (err != nil && err != io.EOF) || reason == "closed" {
		d.logger.Warn("provider stream ended", fields...)
	} else {
		d.logger.Info("provider stream ended", fields...)
	}
}
