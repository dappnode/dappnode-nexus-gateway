package handlers

import (
	"bytes"
	"container/list"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/google/uuid"
	"github.com/tinfoilsh/encrypted-http-body-protocol/identity"
	"github.com/tinfoilsh/encrypted-http-body-protocol/protocol"
)

const (
	confidentialEnvelopeSchema        = 1
	confidentialEnvelopeLimit         = maxRequestBodySize + 64*1024
	confidentialMaxAge                = 2 * time.Minute
	confidentialFutureSkew            = 30 * time.Second
	confidentialReplayTTL             = 3 * time.Minute
	confidentialReplayCapacity        = 100_000
	confidentialReplayAccountCapacity = 10_000
	confidentialMaxEHBPFrame          = 1*1024*1024 + 16
	confidentialMaxEHBPBody           = 16 * 1024 * 1024
)

// ConfidentialChatCompletionsHandler is the strict EHBP-only wrapper around
// the existing OpenAI-compatible chat completions handler. Authentication
// stays in the Authorization header; only the request and response bodies are
// protected by EHBP.
type ConfidentialChatCompletionsHandler struct {
	encrypted http.Handler
	next      http.Handler
	auth      ports.AuthService
	logger    ports.Logger
	replays   *confidentialReplayCache
	now       func() time.Time
}

type confidentialEnvelope struct {
	SchemaVersion int             `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	IssuedAt      string          `json:"issued_at"`
	Request       json.RawMessage `json:"request"`
}

// NewConfidentialChatCompletionsHandler creates a strict encrypted handler.
// serverIdentity must contain the process-local private key generated inside
// the enclave; a public-only identity is intentionally rejected.
func NewConfidentialChatCompletionsHandler(
	serverIdentity *identity.Identity,
	auth ports.AuthService,
	next http.Handler,
	logger ports.Logger,
) (*ConfidentialChatCompletionsHandler, error) {
	if serverIdentity == nil || serverIdentity.PrivateKey() == nil {
		return nil, errors.New("confidential handler requires a private EHBP identity")
	}
	if auth == nil {
		return nil, errors.New("confidential handler requires authentication")
	}
	if next == nil {
		return nil, errors.New("confidential handler requires a downstream handler")
	}

	h := &ConfidentialChatCompletionsHandler{
		next:    next,
		auth:    auth,
		logger:  logger,
		replays: newConfidentialReplayCache(confidentialReplayTTL, confidentialReplayCapacity),
		now:     time.Now,
	}
	h.encrypted = serverIdentity.Middleware()(http.HandlerFunc(h.handleDecrypted))
	return h, nil
}

// Handle rejects plaintext before invoking the EHBP middleware. The upstream
// library deliberately supports plaintext fallback, so the confidential route
// must enforce strict mode itself.
func (h *ConfidentialChatCompletionsHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")

	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("confidential request body is required"))
		return
	}
	encapsulatedKeys := r.Header.Values(protocol.EncapsulatedKeyHeader)
	if len(encapsulatedKeys) == 0 {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("confidential endpoint requires an EHBP-encrypted body"))
		return
	}
	if len(encapsulatedKeys) != 1 || !isCanonicalEHBPKey(encapsulatedKeys[0]) {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("invalid EHBP encapsulated key header"))
		return
	}
	if r.ContentLength > confidentialMaxEHBPBody {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("encrypted request body exceeds the maximum size"))
		return
	}

	// EHBP v0.2.6 allocates each declared ciphertext frame before reading it.
	// Validate framing and the aggregate wire size first so an unauthenticated
	// length prefix cannot trigger an attacker-controlled allocation.
	r.Body = newValidatedEHBPBody(r.Body, confidentialMaxEHBPFrame, confidentialMaxEHBPBody)

	h.encrypted.ServeHTTP(w, r)
}

func isCanonicalEHBPKey(encoded string) bool {
	if len(encoded) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == encoded
}

// handleDecrypted executes only after the EHBP middleware authenticates the
// first encrypted frame and installs response encryption.
func (h *ConfidentialChatCompletionsHandler) handleDecrypted(w http.ResponseWriter, r *http.Request) {
	token, err := ExtractBearerToken(r)
	if err != nil {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, err)
		return
	}
	auth, err := h.auth.AuthenticateAPIKey(r.Context(), token)
	if err != nil {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, err)
		return
	}
	if auth.Account.ID == "" {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInternal("authentication returned no account"))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, confidentialEnvelopeLimit+1))
	if err != nil {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("failed to read confidential request"))
		return
	}
	if len(body) > confidentialEnvelopeLimit {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("confidential request body exceeds the maximum size"))
		return
	}

	envelope, err := decodeConfidentialEnvelope(body)
	if err != nil {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField(err.Error()))
		return
	}

	now := h.now()
	issuedAt, err := time.Parse(time.RFC3339, envelope.IssuedAt)
	if err != nil {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("issued_at must be an RFC3339 timestamp"))
		return
	}
	if issuedAt.Before(now.Add(-confidentialMaxAge)) {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("confidential request is stale"))
		return
	}
	if issuedAt.After(now.Add(confidentialFutureSkew)) {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("confidential request timestamp is in the future"))
		return
	}

	parsedID, err := uuid.Parse(envelope.RequestID)
	if err != nil || parsedID == uuid.Nil || parsedID.String() != envelope.RequestID {
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrInvalidField("request_id must be a canonical UUID"))
		return
	}

	switch h.replays.checkAndStore(auth.Account.ID, envelope.RequestID, h.now) {
	case replayDuplicate:
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrConflict("confidential request was already processed"))
		return
	case replayAccountCapacityReached:
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, &domain.GatewayError{
			HTTPStatus: http.StatusTooManyRequests, Type: domain.ErrTypeRateLimit,
			Code: domain.ErrCodeConfidentialLimit, Message: "Account confidential request limit reached; retry after replay entries expire",
		})
		return
	case replayCapacityReached:
		rejectRequest(w, r, h.logger, domain.EndpointChatCompletions, domain.ErrProviderUnavailable("confidential replay protection").WithMeta(
			"fault", domain.FaultNexus, "dependency", "replay_store", "cause", domain.CauseReplayStoreFull))
		return
	}

	// The downstream handler rechecks authentication and remains responsible for
	// OpenAI validation, generation, and streaming. Only replace the
	// decrypted envelope with its exact nested OpenAI request object.
	r.Body = io.NopCloser(bytes.NewReader(envelope.Request))
	r.ContentLength = int64(len(envelope.Request))
	r.Header.Del("Content-Length")
	r.Header.Del("Transfer-Encoding")
	r.TransferEncoding = nil
	h.next.ServeHTTP(w, r)
}

func decodeConfidentialEnvelope(body []byte) (confidentialEnvelope, error) {
	var envelope confidentialEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, errors.New("invalid confidential request envelope")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return envelope, errors.New("invalid confidential request envelope")
	}
	if envelope.SchemaVersion != confidentialEnvelopeSchema {
		return envelope, errors.New("unsupported confidential request schema_version")
	}
	trimmedRequest := bytes.TrimSpace(envelope.Request)
	if len(trimmedRequest) < 2 || trimmedRequest[0] != '{' || trimmedRequest[len(trimmedRequest)-1] != '}' || !json.Valid(trimmedRequest) {
		return envelope, errors.New("request must be a JSON object")
	}
	envelope.Request = append(json.RawMessage(nil), trimmedRequest...)
	return envelope, nil
}

type replayResult uint8

const (
	replayAccepted replayResult = iota
	replayDuplicate
	replayCapacityReached
	replayAccountCapacityReached
)

type confidentialReplayKey struct{ accountID, requestID string }
type confidentialReplayEntry struct {
	key       confidentialReplayKey
	expiresAt time.Time
}

type confidentialReplayCache struct {
	// ponytail: one lock for a bounded cache; shard by account if contention is measured.
	mu              sync.Mutex
	ttl             time.Duration
	capacity        int
	accountCapacity int
	entries         map[confidentialReplayKey]struct{}
	accounts        map[string]int
	expirations     *list.List
}

func newConfidentialReplayCache(ttl time.Duration, capacity int) *confidentialReplayCache {
	return &confidentialReplayCache{
		ttl: ttl, capacity: capacity, accountCapacity: min(capacity, confidentialReplayAccountCapacity),
		entries: make(map[confidentialReplayKey]struct{}), accounts: make(map[string]int), expirations: list.New(),
	}
}

func (c *confidentialReplayCache) checkAndStore(accountID, requestID string, clock func() time.Time) replayResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := clock() // Sample under the lock so expiry order follows insertion order.
	// Fixed TTLs preserve insertion order. Remove only expired entries, never
	// live replay protection; full-cache rejection does not scan the map.
	for front := c.expirations.Front(); front != nil; front = c.expirations.Front() {
		entry := front.Value.(confidentialReplayEntry)
		if now.Before(entry.expiresAt) {
			break
		}
		delete(c.entries, entry.key)
		c.accounts[entry.key.accountID]--
		if c.accounts[entry.key.accountID] == 0 {
			delete(c.accounts, entry.key.accountID)
		}
		c.expirations.Remove(front)
	}
	key := confidentialReplayKey{accountID: accountID, requestID: requestID}
	if _, ok := c.entries[key]; ok {
		return replayDuplicate
	}
	if c.accounts[accountID] >= c.accountCapacity {
		return replayAccountCapacityReached
	}
	if len(c.entries) >= c.capacity {
		return replayCapacityReached
	}
	c.entries[key] = struct{}{}
	c.accounts[accountID]++
	c.expirations.PushBack(confidentialReplayEntry{key: key, expiresAt: now.Add(c.ttl)})
	return replayAccepted
}

type validatedEHBPBody struct {
	source         io.ReadCloser
	maxFrame       uint32
	maxBody        uint64
	wireSize       uint64
	prefix         [4]byte
	prefixOffset   int
	frameRemaining uint32
}

func newValidatedEHBPBody(source io.ReadCloser, maxFrame, maxBody int) io.ReadCloser {
	return &validatedEHBPBody{
		source:       source,
		maxFrame:     uint32(maxFrame),
		maxBody:      uint64(maxBody),
		prefixOffset: 4,
	}
}

func (r *validatedEHBPBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	if r.prefixOffset < len(r.prefix) {
		n := copy(p, r.prefix[r.prefixOffset:])
		r.prefixOffset += n
		return n, nil
	}

	if r.frameRemaining > 0 {
		limit := min(len(p), int(r.frameRemaining))
		n, err := r.source.Read(p[:limit])
		r.frameRemaining -= uint32(n)
		if n == 0 && err == nil {
			return 0, io.ErrNoProgress
		}
		if err == io.EOF && r.frameRemaining > 0 {
			return n, io.ErrUnexpectedEOF
		}
		return n, err
	}

	_, err := io.ReadFull(r.source, r.prefix[:])
	if err != nil {
		return 0, err
	}
	frameSize := binary.BigEndian.Uint32(r.prefix[:])
	if frameSize == 0 {
		return 0, errors.New("EHBP frame must not be empty")
	}
	if frameSize > r.maxFrame {
		return 0, errors.New("EHBP frame exceeds the maximum size")
	}
	declaredWireSize := uint64(len(r.prefix)) + uint64(frameSize)
	if declaredWireSize > r.maxBody-r.wireSize {
		return 0, errors.New("EHBP body exceeds the maximum size")
	}
	r.wireSize += declaredWireSize
	r.prefixOffset = 0
	r.frameRemaining = frameSize

	n := copy(p, r.prefix[:])
	r.prefixOffset = n
	return n, nil
}

func (r *validatedEHBPBody) Close() error {
	return r.source.Close()
}
