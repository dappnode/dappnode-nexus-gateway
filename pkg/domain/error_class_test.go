package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestFaultOf(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ErrUnsupportedModel("x"), FaultCaller},
		{ErrInsufficientBalance(), FaultCaller},
		{ErrClientCanceled(), FaultCaller},
		{ErrProviderUnavailable("p"), FaultProvider},
		{ErrProviderTimeout("p"), FaultProvider},
		{&GatewayError{HTTPStatus: 400, Code: ErrCodeProviderRejectedRequest}, FaultUnclear},
		{&GatewayError{HTTPStatus: 502, Code: ErrCodeProviderConfiguration}, FaultNexus},
		{ErrInternal("x"), FaultNexus},
		{errors.New("untyped"), FaultNexus},
		{&GatewayError{HTTPStatus: 500, Code: "something_new"}, FaultNexus},
	} {
		if got := FaultOf(tc.err); got != tc.want {
			t.Errorf("FaultOf(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// Only classification and log-safe metadata are logged: messages and free
// text that can echo a prompt never are.
func TestDiagnosticFieldsKeepOnlyLogSafeMetadata(t *testing.T) {
	err := ErrInternal("an internal error occurred").WithMeta(
		"dependency", "pii",
		"cause", CausePIIFilterTimeout,
		"upstream_status", 503,
		"upstream_code", strings.Repeat("x", 300),
		"upstream_error", "the prompt said: my password is hunter2",
		"reason", "free text",
	)
	got := map[string]any{}
	fields := DiagnosticFields(err)
	for i := 0; i+1 < len(fields); i += 2 {
		got[fields[i].(string)] = fields[i+1]
	}
	for key, want := range map[string]any{
		"fault": FaultNexus, "error_code": ErrCodeInternalError, "gateway_status": 500,
		"dependency": "pii", "cause": CausePIIFilterTimeout, "upstream_status": 503,
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if code, _ := got["upstream_code"].(string); len(code) != maxLogValue {
		t.Errorf("upstream_code length = %d, want %d", len(code), maxLogValue)
	}
	for _, key := range []string{"upstream_error", "reason"} {
		if _, ok := got[key]; ok {
			t.Errorf("%s was logged", key)
		}
	}
}

func TestUpstreamStatusAndAnnotate(t *testing.T) {
	err := Annotate(ErrProviderUnavailable("p"), "upstream_status", 503)
	if got := UpstreamStatus(err); got != 503 {
		t.Fatalf("UpstreamStatus = %d", got)
	}
	if got := UpstreamStatus(errors.New("x")); got != 0 {
		t.Fatalf("UpstreamStatus(untyped) = %d", got)
	}
	plain := errors.New("x")
	if Annotate(plain, "a", 1) != plain {
		t.Fatal("Annotate changed an untyped error")
	}
}
