// Package cache remembers PII analyzer results across requests.
//
// Chat clients resend the whole conversation on every turn, and the analyzer
// is called once per distinct text field. Without a shared cache, turn N of a
// conversation re-analyzes all N-1 earlier messages. The cache keys on an
// HMAC-SHA256 of the options and text under a random key drawn at start and
// kept only in memory, so it holds keyed digests and entity offsets, never the text,
// and its keys cannot be matched against guessed texts outside the process.
package cache

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"sync/atomic"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// DefaultEntries bounds the cache when no size is configured. An entry is a
// 32-byte key plus a few entity offsets, so this stays around a few megabytes.
const DefaultEntries = 20000

// Filter wraps a PII filter with a bounded least-recently-used cache.
type Filter struct {
	next   ports.PIIFilter
	max    int
	secret []byte // HMAC key for cache keys; random per process.

	mu    sync.Mutex
	order *list.List // Front is the most recently used.
	items map[[sha256.Size]byte]*list.Element

	hits   atomic.Int64
	misses atomic.Int64
}

type entry struct {
	key      [sha256.Size]byte
	entities []domain.PIIEntity
}

// New returns next wrapped in a cache of at most maxEntries results.
func New(next ports.PIIFilter, maxEntries int) *Filter {
	if maxEntries <= 0 {
		maxEntries = DefaultEntries
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("pii cache: no randomness for the cache key: " + err.Error())
	}
	return &Filter{
		next:   next,
		max:    maxEntries,
		secret: secret,
		order:  list.New(),
		items:  make(map[[sha256.Size]byte]*list.Element),
	}
}

// Enabled reports whether the wrapped filter is operational.
func (f *Filter) Enabled() bool { return f.next.Enabled() }

// Analyze returns cached entities for text analyzed before with the same
// options, and asks the wrapped filter otherwise. Errors are never cached.
func (f *Filter) Analyze(ctx context.Context, text string, opts ports.PIIAnalyzeOptions) ([]domain.PIIEntity, error) {
	if text == "" {
		return nil, nil
	}
	key := f.cacheKey(text, opts)
	if entities, ok := f.get(key); ok {
		f.hits.Add(1)
		return entities, nil
	}
	f.misses.Add(1)
	entities, err := f.next.Analyze(ctx, text, opts)
	if err != nil {
		return nil, err
	}
	f.put(key, entities)
	return clone(entities), nil
}

// Stats returns how many lookups were answered from the cache and how many
// went to the analyzer.
func (f *Filter) Stats() (hits, misses int64) {
	return f.hits.Load(), f.misses.Load()
}

func (f *Filter) get(key [sha256.Size]byte) ([]domain.PIIEntity, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	el, ok := f.items[key]
	if !ok {
		return nil, false
	}
	f.order.MoveToFront(el)
	return clone(el.Value.(*entry).entities), true
}

func (f *Filter) put(key [sha256.Size]byte, entities []domain.PIIEntity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if el, ok := f.items[key]; ok {
		f.order.MoveToFront(el)
		return
	}
	f.items[key] = f.order.PushFront(&entry{key: key, entities: clone(entities)})
	for f.order.Len() > f.max {
		oldest := f.order.Back()
		f.order.Remove(oldest)
		delete(f.items, oldest.Value.(*entry).key)
	}
}

// cacheKey separates fields with a zero byte, which cannot appear in a mode
// or language code, so different option sets never share a key.
func (f *Filter) cacheKey(text string, opts ports.PIIAnalyzeOptions) [sha256.Size]byte {
	h := hmac.New(sha256.New, f.secret)
	h.Write([]byte(opts.Mode))
	h.Write([]byte{0})
	h.Write([]byte(opts.Language))
	h.Write([]byte{0})
	h.Write([]byte(text))
	var key [sha256.Size]byte
	copy(key[:], h.Sum(nil))
	return key
}

// clone keeps callers from changing what the cache holds.
func clone(entities []domain.PIIEntity) []domain.PIIEntity {
	if entities == nil {
		return nil
	}
	return append([]domain.PIIEntity(nil), entities...)
}
