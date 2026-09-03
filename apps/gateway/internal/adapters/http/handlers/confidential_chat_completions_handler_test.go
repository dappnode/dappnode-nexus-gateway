package handlers

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tinfoilsh/encrypted-http-body-protocol/identity"
	"github.com/tinfoilsh/encrypted-http-body-protocol/protocol"
)

type confidentialTestLogger struct{}

func (*confidentialTestLogger) Debug(string, ...any) {}
func (*confidentialTestLogger) Info(string, ...any)  {}
func (*confidentialTestLogger) Warn(string, ...any)  {}
func (*confidentialTestLogger) Error(string, ...any) {}

type recordedConfidentialRequest struct {
	authorization string
	body          []byte
	calls         int
}

func TestConfidentialChatCompletionsHandler_DecryptsEnvelopeAndEncryptsResponse(t *testing.T) {
	recorded := &recordedConfidentialRequest{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded.calls++
		recorded.authorization = r.Header.Get("Authorization")
		recorded.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response-1"}`)
	})
	handler, clientIdentity := newConfidentialTestHandler(t, next)
	openAIRequest := json.RawMessage(`{"model":"test","messages":[{"role":"user","content":"unique-canary-prompt"}]}`)
	request, requestContext, ciphertext := newEncryptedConfidentialRequest(t, clientIdentity, envelopeAt(time.Now(), uuid.NewString(), openAIRequest))

	if bytes.Contains(ciphertext, []byte("unique-canary-prompt")) {
		t.Fatal("encrypted request exposed prompt plaintext")
	}
	if bytes.Contains(ciphertext, []byte("nexus-test-key")) {
		t.Fatal("encrypted request body unexpectedly included the bearer API key")
	}
	if got := request.Header.Get("Authorization"); got != "Bearer nexus-test-key" {
		t.Fatalf("wire Authorization = %q, want visible standard bearer header", got)
	}
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)

	if bytes.Contains(recorder.Body.Bytes(), []byte("response-1")) {
		t.Fatal("encrypted response exposed completion plaintext")
	}
	responseBody := decryptConfidentialResponse(t, recorder, requestContext)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; decrypted body = %s", recorder.Code, responseBody)
	}
	if recorded.calls != 1 {
		t.Fatalf("downstream calls = %d, want 1", recorded.calls)
	}
	if recorded.authorization != "Bearer nexus-test-key" {
		t.Fatalf("Authorization = %q, want unchanged bearer header", recorded.authorization)
	}
	if !bytes.Equal(recorded.body, openAIRequest) {
		t.Fatalf("downstream body = %s, want %s", recorded.body, openAIRequest)
	}
	if string(responseBody) != `{"id":"response-1"}` {
		t.Fatalf("decrypted response = %s", responseBody)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-transform" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestConfidentialChatCompletionsHandler_StreamsEncryptedResponse(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	})
	handler, clientIdentity := newConfidentialTestHandler(t, next)
	request, requestContext, _ := newEncryptedConfidentialRequest(t, clientIdentity, envelopeAt(
		time.Now(), uuid.NewString(), json.RawMessage(`{"model":"test","stream":true,"messages":[]}`),
	))
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if bytes.Contains(recorder.Body.Bytes(), []byte("data: first")) {
		t.Fatal("encrypted stream exposed SSE plaintext")
	}
	responseBody := decryptConfidentialResponse(t, recorder, requestContext)
	if got, want := string(responseBody), "data: first\n\ndata: [DONE]\n\n"; got != want {
		t.Fatalf("decrypted stream = %q, want %q", got, want)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
}

func TestConfidentialChatCompletionsHandler_EncryptsAuthenticationErrors(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := ExtractBearerToken(r); err != nil {
			WriteError(w, err)
			return
		}
		t.Fatal("missing bearer token was accepted")
	})
	handler, clientIdentity := newConfidentialTestHandler(t, next)
	request, requestContext, _ := newEncryptedConfidentialRequest(t, clientIdentity, envelopeAt(
		time.Now(), uuid.NewString(), json.RawMessage(`{"model":"test"}`),
	))
	request.Header.Del("Authorization")
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if bytes.Contains(recorder.Body.Bytes(), []byte("missing Authorization")) {
		t.Fatal("authentication error was returned in plaintext")
	}
	responseBody := decryptConfidentialResponse(t, recorder, requestContext)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if !bytes.Contains(responseBody, []byte(`"code":"invalid_api_key"`)) {
		t.Fatalf("decrypted authentication error = %s", responseBody)
	}
}

func TestConfidentialChatCompletionsHandler_RejectsPlaintext(t *testing.T) {
	calls := 0
	handler, _ := newConfidentialTestHandler(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	request := httptest.NewRequest(http.MethodPost, "/v1/confidential/chat/completions", strings.NewReader(`{"model":"test"}`))
	request.Header.Set("Authorization", "Bearer nexus-test-key")
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if calls != 0 {
		t.Fatalf("downstream calls = %d, want 0", calls)
	}
	if recorder.Header().Get(protocol.ResponseNonceHeader) != "" {
		t.Fatal("plaintext rejection unexpectedly had an encrypted response nonce")
	}
}

func TestConfidentialChatCompletionsHandler_RejectsInvalidFreshnessAndReplay(t *testing.T) {
	tests := []struct {
		name       string
		envelope   []byte
		wantStatus int
	}{
		{
			name:       "stale",
			envelope:   envelopeAt(time.Now().Add(-confidentialMaxAge-time.Second), uuid.NewString(), json.RawMessage(`{"model":"test"}`)),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "future",
			envelope:   envelopeAt(time.Now().Add(confidentialFutureSkew+time.Second), uuid.NewString(), json.RawMessage(`{"model":"test"}`)),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "non-canonical request ID",
			envelope:   envelopeAt(time.Now(), strings.ToUpper(uuid.NewString()), json.RawMessage(`{"model":"test"}`)),
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler, clientIdentity := newConfidentialTestHandler(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			request, requestContext, _ := newEncryptedConfidentialRequest(t, clientIdentity, test.envelope)
			recorder := httptest.NewRecorder()

			handler.Handle(recorder, request)
			_ = decryptConfidentialResponse(t, recorder, requestContext)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			if calls != 0 {
				t.Fatalf("downstream calls = %d, want 0", calls)
			}
		})
	}

	calls := 0
	handler, clientIdentity := newConfidentialTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, "ok")
	}))
	requestID := uuid.NewString()
	for attempt, wantStatus := range []int{http.StatusOK, http.StatusConflict} {
		request, requestContext, _ := newEncryptedConfidentialRequest(t, clientIdentity, envelopeAt(
			time.Now(), requestID, json.RawMessage(`{"model":"test"}`),
		))
		recorder := httptest.NewRecorder()
		handler.Handle(recorder, request)
		_ = decryptConfidentialResponse(t, recorder, requestContext)
		if recorder.Code != wantStatus {
			t.Fatalf("attempt %d status = %d, want %d", attempt+1, recorder.Code, wantStatus)
		}
	}
	if calls != 1 {
		t.Fatalf("downstream calls = %d, want exactly 1", calls)
	}
}

func TestConfidentialChatCompletionsHandler_RejectsTamperingAndStaleKey(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*http.Request, []byte)
		wantStatus int
	}{
		{
			name: "duplicate encapsulated key header",
			mutate: func(request *http.Request, _ []byte) {
				request.Header.Add(protocol.EncapsulatedKeyHeader, request.Header.Get(protocol.EncapsulatedKeyHeader))
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "non-canonical encapsulated key header",
			mutate: func(request *http.Request, _ []byte) {
				request.Header.Set(protocol.EncapsulatedKeyHeader, strings.Repeat("A", 64))
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "tampered ciphertext",
			mutate: func(request *http.Request, ciphertext []byte) {
				ciphertext[len(ciphertext)-1] ^= 0xff
				request.Body = io.NopCloser(bytes.NewReader(ciphertext))
			},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "oversized frame prefix",
			mutate: func(request *http.Request, _ []byte) {
				prefix := make([]byte, 4)
				binary.BigEndian.PutUint32(prefix, confidentialMaxEHBPFrame+1)
				request.Body = io.NopCloser(bytes.NewReader(prefix))
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "zero frame prefix",
			mutate: func(request *http.Request, _ []byte) {
				request.Body = io.NopCloser(bytes.NewReader(make([]byte, 4)))
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "truncated frame",
			mutate: func(request *http.Request, _ []byte) {
				wire := make([]byte, 5)
				binary.BigEndian.PutUint32(wire[:4], 32)
				request.Body = io.NopCloser(bytes.NewReader(wire))
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			handler, clientIdentity := newConfidentialTestHandler(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			request, _, ciphertext := newEncryptedConfidentialRequest(t, clientIdentity, envelopeAt(
				time.Now(), uuid.NewString(), json.RawMessage(`{"model":"test"}`),
			))
			test.mutate(request, ciphertext)
			recorder := httptest.NewRecorder()

			handler.Handle(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if calls != 0 {
				t.Fatalf("downstream calls = %d, want 0", calls)
			}
		})
	}

	serverIdentity, err := identity.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewConfidentialChatCompletionsHandler(serverIdentity, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("stale-key request reached downstream")
	}), &confidentialTestLogger{})
	if err != nil {
		t.Fatal(err)
	}
	staleIdentity, err := identity.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	staleClient, err := identity.FromPublicKeyBytes(staleIdentity.MarshalPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	request, _, _ := newEncryptedConfidentialRequest(t, staleClient, envelopeAt(
		time.Now(), uuid.NewString(), json.RawMessage(`{"model":"test"}`),
	))
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("stale-key status = %d, want 422; body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != protocol.ProblemJSONMediaType {
		t.Fatalf("stale-key Content-Type = %q, want %q", got, protocol.ProblemJSONMediaType)
	}
}

func TestNewConfidentialChatCompletionsHandler_RequiresPrivateIdentity(t *testing.T) {
	serverIdentity, err := identity.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	publicOnly, err := identity.FromPublicKeyBytes(serverIdentity.MarshalPublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewConfidentialChatCompletionsHandler(publicOnly, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), &confidentialTestLogger{}); err == nil {
		t.Fatal("public-only identity was accepted")
	}
}

func newConfidentialTestHandler(t *testing.T, next http.Handler) (*ConfidentialChatCompletionsHandler, *identity.Identity) {
	t.Helper()
	serverIdentity, err := identity.NewIdentity()
	if err != nil {
		t.Fatalf("generate server identity: %v", err)
	}
	handler, err := NewConfidentialChatCompletionsHandler(serverIdentity, next, &confidentialTestLogger{})
	if err != nil {
		t.Fatalf("create confidential handler: %v", err)
	}
	clientIdentity, err := identity.FromPublicKeyBytes(serverIdentity.MarshalPublicKey())
	if err != nil {
		t.Fatalf("create client identity: %v", err)
	}
	return handler, clientIdentity
}

func newEncryptedConfidentialRequest(
	t *testing.T,
	clientIdentity *identity.Identity,
	envelope []byte,
) (*http.Request, *identity.RequestContext, []byte) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/confidential/chat/completions", bytes.NewReader(envelope))
	request.Header.Set("Authorization", "Bearer nexus-test-key")
	request.Header.Set("Content-Type", "application/json")
	requestContext, err := clientIdentity.EncryptRequestWithContext(request)
	if err != nil {
		t.Fatalf("encrypt request: %v", err)
	}
	ciphertext, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("materialize ciphertext: %v", err)
	}
	request.Body = io.NopCloser(bytes.NewReader(ciphertext))
	return request, requestContext, ciphertext
}

func decryptConfidentialResponse(t *testing.T, recorder *httptest.ResponseRecorder, requestContext *identity.RequestContext) []byte {
	t.Helper()
	response := recorder.Result()
	if response.Header.Get(protocol.ResponseNonceHeader) == "" {
		t.Fatalf("encrypted response is missing %s; body = %s", protocol.ResponseNonceHeader, recorder.Body.String())
	}
	if err := requestContext.DecryptResponse(response); err != nil {
		t.Fatalf("decrypt response: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read decrypted response: %v", err)
	}
	return body
}

func envelopeAt(issuedAt time.Time, requestID string, request json.RawMessage) []byte {
	body, err := json.Marshal(confidentialEnvelope{
		SchemaVersion: confidentialEnvelopeSchema,
		RequestID:     requestID,
		IssuedAt:      issuedAt.UTC().Format(time.RFC3339Nano),
		Request:       request,
	})
	if err != nil {
		panic(err)
	}
	return body
}
