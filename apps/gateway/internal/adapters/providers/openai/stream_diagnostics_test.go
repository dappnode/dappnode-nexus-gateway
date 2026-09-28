package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/middleware"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type diagnosticLog struct {
	level, message string
	fields         map[string]any
}
type diagnosticLogger struct{ entries []diagnosticLog }

func (l *diagnosticLogger) add(level, message string, fields ...any) {
	m := map[string]any{}
	for i := 0; i+1 < len(fields); i += 2 {
		m[fields[i].(string)] = fields[i+1]
	}
	l.entries = append(l.entries, diagnosticLog{level, message, m})
}
func (l *diagnosticLogger) Debug(m string, f ...any) { l.add("debug", m, f...) }
func (l *diagnosticLogger) Info(m string, f ...any)  { l.add("info", m, f...) }
func (l *diagnosticLogger) Warn(m string, f ...any)  { l.add("warn", m, f...) }
func (l *diagnosticLogger) Error(m string, f ...any) { l.add("error", m, f...) }

type terminalErrorReader struct{ err error }

func (r terminalErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestStreamDiagnostics_TerminationAndUsage(t *testing.T) {
	finish := `data: {"id":"provider-id","choices":[{"delta":{"content":"SECRET-CONTENT"},"finish_reason":"tool_calls"}]}` + "\n\n"
	usage := `data: {"id":"provider-id","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}` + "\n\n"
	for _, tc := range []struct {
		name, data, end, level string
		usage                  bool
		malformed, unsupported int
		readErr                error
	}{
		{name: "trailing usage", data: finish + usage + "data: [DONE]\n", end: "done_marker", level: "info", usage: true},
		{name: "provider omission", data: finish + "data: [DONE]\n", end: "done_marker", level: "warn"},
		{name: "bare eof", data: finish, end: "eof", level: "warn"},
		{name: "malformed", data: finish + "data: {SECRET-MALFORMED}\n" + usage + "data: [DONE]\n", end: "done_marker", level: "warn", usage: true, malformed: 1},
		{name: "unsupported framing", data: finish + "{SECRET-FRAMING}\n", end: "eof", level: "warn", unsupported: 1},
		{name: "data without space", data: finish + "data:{SECRET-FRAMING}\n", end: "eof", level: "warn", malformed: 1},
		{name: "canceled", data: finish, end: "canceled", level: "warn", readErr: context.Canceled},
		{name: "timeout", data: finish, end: "deadline_exceeded", level: "warn", readErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := &diagnosticLogger{}
			reader := io.Reader(strings.NewReader(tc.data))
			if tc.readErr != nil {
				reader = io.MultiReader(reader, terminalErrorReader{tc.readErr})
			}
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "gateway-id")
			stream := NewStream(&http.Response{Body: io.NopCloser(reader)}).WithDiagnostics(ctx, logger, domain.PublicModel{PublicModelID: "model", ProviderConfig: domain.ProviderConfig{ProviderName: "novita"}}, 1)
			for {
				if _, err := stream.Recv(); err != nil {
					break
				}
			}
			stream.Close()
			stream.Close()
			summaries := 0
			for _, entry := range logger.entries {
				if strings.Contains(fmt.Sprint(entry.fields), "SECRET-") {
					t.Fatal("stream content was logged")
				}
				if entry.message != "provider stream ended" {
					continue
				}
				summaries++
				if entry.level != tc.level || entry.fields["upstream_end"] != tc.end || entry.fields["usage_received"] != tc.usage || entry.fields["malformed_chunks"] != tc.malformed || entry.fields["unsupported_data_lines"] != tc.unsupported {
					t.Fatalf("unexpected summary: %+v", entry)
				}
				if entry.fields["request_id"] != "gateway-id" || entry.fields["provider_request_id"] != "provider-id" {
					t.Fatalf("missing correlation: %+v", entry.fields)
				}
				if tc.usage && (entry.fields["usage_chunks"] != 1 || entry.fields["mapped_usage_chunks"] != 1) {
					t.Fatalf("usage counters: %+v", entry.fields)
				}
			}
			if summaries != 1 {
				t.Fatalf("summaries=%d want 1", summaries)
			}
		})
	}
}

func TestStreamDiagnostics_UsageOnToolDelta(t *testing.T) {
	logger := &diagnosticLogger{}
	stream := newTestStream(`data: {"id":"provider-id","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"SECRET-ARGUMENTS"}}]},"finish_reason":null}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`+"\n\ndata: [DONE]\n").WithDiagnostics(context.Background(), logger, domain.PublicModel{}, 1)
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	for _, entry := range logger.entries {
		encoded, _ := json.Marshal(entry.fields)
		if strings.Contains(string(encoded), "SECRET-ARGUMENTS") {
			t.Fatal("tool arguments leaked")
		}
		if entry.message == "provider stream chunk metadata" && (entry.fields["usage_forwarded"] != true || entry.fields["has_tool_calls"] != true) {
			t.Fatalf("unexpected chunk metadata: %+v", entry)
		}
	}
}

func TestStreamDiagnosticsUnknownFinishReasonIsRedacted(t *testing.T) {
	logger := &diagnosticLogger{}
	stream := newTestStream(`data: {"id":"provider-id","choices":[{"delta":{},"finish_reason":"PRIVATE-PROMPT-CANARY"}]}`+"\n\ndata: [DONE]\n").WithDiagnostics(context.Background(), logger, domain.PublicModel{}, 1)
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	for _, entry := range logger.entries {
		encoded, err := json.Marshal(entry.fields)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "PRIVATE-PROMPT-CANARY") {
			t.Fatal("finish reason leaked private content")
		}
	}
}
