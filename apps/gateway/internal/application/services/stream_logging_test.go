package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type streamLogEntry struct {
	level, message string
	fields         map[string]any
}
type streamLogRecorder struct{ entries []streamLogEntry }

func (l *streamLogRecorder) add(level, message string, fields ...any) {
	m := map[string]any{}
	for i := 0; i+1 < len(fields); i += 2 {
		m[fields[i].(string)] = fields[i+1]
	}
	l.entries = append(l.entries, streamLogEntry{level, message, m})
}
func (l *streamLogRecorder) Debug(m string, f ...any) { l.add("debug", m, f...) }
func (l *streamLogRecorder) Info(m string, f ...any)  { l.add("info", m, f...) }
func (l *streamLogRecorder) Warn(m string, f ...any)  { l.add("warn", m, f...) }
func (l *streamLogRecorder) Error(m string, f ...any) { l.add("error", m, f...) }

type failingLogMeter struct {
	stubUsageMeter
	err error
}

func (m *failingLogMeter) RecordSuccess(ctx context.Context, id string, a domain.AuthContext, endpoint string, req domain.GenerateRequest, result domain.GenerateResult, model domain.PublicModel, latency int64) error {
	m.stubUsageMeter.RecordSuccess(ctx, id, a, endpoint, req, result, model, latency)
	return m.err
}

func TestStreamLogging_UsageAndMeteringOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name               string
		usage              *domain.Usage
		readErr, meterErr  error
		beforeFinish       bool
		wantMessage, level string
	}{
		{name: "missing", wantMessage: "stream completed without usage", level: "warn"},
		{name: "known", usage: &domain.Usage{PromptTokens: 100, CompletionTokens: 20}, wantMessage: "stream metering completed", level: "info"},
		{name: "canceled after finish", readErr: context.Canceled, wantMessage: "stream interrupted after finish", level: "warn"},
		{name: "metering failed", usage: &domain.Usage{PromptTokens: 100}, meterErr: errors.New("metering unavailable"), wantMessage: "failed to record stream usage", level: "error"},
		{name: "failed before finish", readErr: context.Canceled, beforeFinish: true, wantMessage: "stream error", level: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &streamLogRecorder{}
			meter := &failingLogMeter{err: tc.meterErr}
			finish := "tool_calls"
			event := domain.StreamEvent{Type: domain.StreamEventCompleted, FinishReason: &finish, Usage: tc.usage, ProviderResponseID: "provider-id"}
			if tc.beforeFinish {
				event.Type = domain.StreamEventOutputTextDelta
				event.FinishReason = nil
			}
			steps := []streamStep{{event: event}}
			if tc.readErr != nil {
				steps = append(steps, streamStep{err: tc.readErr})
			}
			stream := &usageTrackingStream{inner: &stubStream{steps: steps}, service: &GenerateService{logger: log, metering: meter}, ctx: context.Background(), requestID: "gateway-id", reservationID: "reservation-id", start: time.Now(), req: domain.GenerateRequest{PublicModelID: "model"}, model: domain.PublicModel{ProviderConfig: domain.ProviderConfig{ProviderName: "novita"}}}
			for {
				if _, err := stream.Recv(); err != nil {
					if tc.readErr == nil && err != io.EOF {
						t.Fatal(err)
					}
					break
				}
			}
			stream.Close()
			found := 0
			for _, entry := range log.entries {
				if entry.message != tc.wantMessage {
					continue
				}
				found++
				if entry.level != tc.level || entry.fields["request_id"] != "gateway-id" || entry.fields["reservation_id"] != "reservation-id" || entry.fields["provider_request_id"] != "provider-id" || entry.fields["usage_received"] != (tc.usage != nil) {
					t.Fatalf("incomplete log: %+v", entry)
				}
			}
			if found != 1 {
				t.Fatalf("found %d logs for %q: %+v", found, tc.wantMessage, log.entries)
			}
			if tc.beforeFinish {
				if meter.failureCalls != 1 || meter.successCalls != 0 {
					t.Fatal("logging changed failure accounting")
				}
			} else if meter.successCalls != 1 {
				t.Fatal("logging changed completion accounting")
			}
		})
	}
}

func TestGenerationErrorLogsOmitProviderEcho(t *testing.T) {
	const secret = "PRIVATE-PROMPT-CANARY"
	err := domain.ErrProviderError(502, secret).WithMeta("upstream_error", secret, "upstream_status", 400, secret, secret)
	service := &GenerateService{}
	fields := service.buildErrorLogFields(context.Background(), "request-id", nil, "chat", "model", domain.PublicModel{}, err, 10)
	encoded, marshalErr := json.Marshal(fields)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("private data leaked: %s", encoded)
	}
	if !strings.Contains(string(encoded), "upstream_status") {
		t.Fatal("missing upstream status")
	}
}
