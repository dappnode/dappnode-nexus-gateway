package services

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// Idempotency-Key handling for requests that charge per result (image
// generation). Like Stripe's: the first request with a key runs; a retry
// while it runs is refused; a retry after it succeeded gets the same result
// without running or charging again. A key is bound to the account and the
// request body that first used it. A request that failed was not charged, so
// its key can be retried.
//
// Results are kept in memory only, for a short time and up to a size limit:
// the gateway stores no media. After that a retry with the key is refused
// rather than charged twice, for as long as the key is remembered.
const (
	idempotencyReplayTTL   = 15 * time.Minute
	idempotencyKeyTTL      = 24 * time.Hour
	idempotencyReplayBytes = 256 << 20
	maxIdempotencyKeyLen   = 255
)

type idempotencyStore struct {
	mu          sync.Mutex
	entries     map[[32]byte]*idempotentRequest
	replayBytes int64
	now         func() time.Time
}

type idempotentRequest struct {
	key      [32]byte
	bodyHash [32]byte
	done     bool
	result   []byte    // Replayable result, while kept.
	replayAt time.Time // Until when result is kept.
	expires  time.Time // Until when the key is remembered.
}

func newIdempotencyStore() *idempotencyStore {
	return &idempotencyStore{entries: map[[32]byte]*idempotentRequest{}, now: time.Now}
}

// begin claims key for a request. It returns the stored result when the key
// already succeeded, and an error when the key is in use or was used for a
// different request.
func (s *idempotencyStore) begin(accountID, key string, body []byte) (*idempotentRequest, []byte, error) {
	if len(key) > maxIdempotencyKeyLen {
		return nil, nil, domain.ErrInvalidField("Idempotency-Key must be at most 255 characters")
	}
	id := sha256.Sum256([]byte(accountID + "\x00" + key))
	bodyHash := sha256.Sum256(canonicalJSON(body))
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expire(now)
	if entry, ok := s.entries[id]; ok {
		switch {
		case entry.bodyHash != bodyHash:
			return nil, nil, domain.ErrIdempotencyKeyReused("This Idempotency-Key was already used for a different request.")
		case !entry.done:
			return nil, nil, domain.ErrIdempotencyInProgress()
		case entry.result != nil:
			return nil, entry.result, nil
		default:
			return nil, nil, domain.ErrIdempotencyKeyReused("This Idempotency-Key was already used for a finished request whose result is no longer kept.")
		}
	}
	entry := &idempotentRequest{key: id, bodyHash: bodyHash, expires: now.Add(idempotencyKeyTTL)}
	s.entries[id] = entry
	return entry, nil, nil
}

// finish records a successful request and keeps its result for replay while
// it fits.
func (s *idempotencyStore) finish(entry *idempotentRequest, result []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	entry.done = true
	entry.expires = now.Add(idempotencyKeyTTL)
	s.expire(now)
	if int64(len(result)) > idempotencyReplayBytes/4 {
		return
	}
	for s.replayBytes+int64(len(result)) > idempotencyReplayBytes && s.dropOldestResult() {
	}
	entry.result = result
	entry.replayAt = now.Add(idempotencyReplayTTL)
	s.replayBytes += int64(len(result))
}

// abandon releases a key whose request failed, so it can be retried.
func (s *idempotencyStore) abandon(entry *idempotentRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[entry.key] == entry {
		delete(s.entries, entry.key)
	}
}

func (s *idempotencyStore) expire(now time.Time) {
	for id, entry := range s.entries {
		if entry.result != nil && now.After(entry.replayAt) {
			s.replayBytes -= int64(len(entry.result))
			entry.result = nil
		}
		if entry.done && now.After(entry.expires) {
			delete(s.entries, id)
		}
	}
}

func (s *idempotencyStore) dropOldestResult() bool {
	var oldest *idempotentRequest
	for _, entry := range s.entries {
		if entry.result != nil && (oldest == nil || entry.replayAt.Before(oldest.replayAt)) {
			oldest = entry
		}
	}
	if oldest == nil {
		return false
	}
	s.replayBytes -= int64(len(oldest.result))
	oldest.result = nil
	return true
}

// canonicalJSON is body with its object keys sorted and whitespace removed,
// so the same request sent by another client library matches its key. A
// body that isn't JSON is compared as it is.
func canonicalJSON(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

// accountSlots limits how many slow jobs one account runs at once.
type accountSlots struct {
	limit   int
	mu      sync.Mutex
	running map[string]int
}

func newAccountSlots(limit int) *accountSlots {
	return &accountSlots{limit: limit, running: map[string]int{}}
}

// acquire takes a slot for account; release returns it.
func (a *accountSlots) acquire(account string) (release func(), ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running[account] >= a.limit {
		return nil, false
	}
	a.running[account]++
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.running[account]--; a.running[account] <= 0 {
				delete(a.running, account)
			}
		})
	}, true
}
