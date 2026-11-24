package cache

import (
	"context"
	"errors"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type countingFilter struct {
	calls int
	err   error
}

func (c *countingFilter) Enabled() bool { return true }

func (c *countingFilter) Analyze(_ context.Context, text string, _ ports.PIIAnalyzeOptions) ([]domain.PIIEntity, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return []domain.PIIEntity{{Type: "PERSON", Start: 0, End: len(text)}}, nil
}

var balanced = ports.PIIAnalyzeOptions{Language: "en", Mode: domain.APIKeyPIIModeBalanced}

func TestRepeatedTextIsAnalyzedOnce(t *testing.T) {
	next := &countingFilter{}
	f := New(next, 10)
	for i := 0; i < 3; i++ {
		got, err := f.Analyze(context.Background(), "Alice", balanced)
		if err != nil || len(got) != 1 || got[0].End != 5 {
			t.Fatalf("call %d: got %v, %v", i, got, err)
		}
	}
	if next.calls != 1 {
		t.Fatalf("analyzer calls = %d, want 1", next.calls)
	}
	if hits, misses := f.Stats(); hits != 2 || misses != 1 {
		t.Fatalf("stats = %d hits, %d misses", hits, misses)
	}
}

func TestOptionsAreNotShared(t *testing.T) {
	next := &countingFilter{}
	f := New(next, 10)
	_, _ = f.Analyze(context.Background(), "Alice", balanced)
	_, _ = f.Analyze(context.Background(), "Alice", ports.PIIAnalyzeOptions{Language: "en", Mode: domain.APIKeyPIIModeHigh})
	_, _ = f.Analyze(context.Background(), "Alice", ports.PIIAnalyzeOptions{Language: "es", Mode: domain.APIKeyPIIModeBalanced})
	if next.calls != 3 {
		t.Fatalf("analyzer calls = %d, want 3", next.calls)
	}
}

func TestErrorsAreNotCached(t *testing.T) {
	next := &countingFilter{err: errors.New("down")}
	f := New(next, 10)
	if _, err := f.Analyze(context.Background(), "Alice", balanced); err == nil {
		t.Fatal("want error")
	}
	next.err = nil
	if _, err := f.Analyze(context.Background(), "Alice", balanced); err != nil {
		t.Fatal(err)
	}
	if next.calls != 2 {
		t.Fatalf("analyzer calls = %d, want 2", next.calls)
	}
}

func TestLeastRecentlyUsedIsEvicted(t *testing.T) {
	next := &countingFilter{}
	f := New(next, 2)
	ctx := context.Background()
	_, _ = f.Analyze(ctx, "a", balanced)
	_, _ = f.Analyze(ctx, "b", balanced)
	_, _ = f.Analyze(ctx, "a", balanced) // a is now the most recent
	_, _ = f.Analyze(ctx, "c", balanced) // evicts b
	_, _ = f.Analyze(ctx, "a", balanced)
	if next.calls != 3 {
		t.Fatalf("after eviction, analyzer calls = %d, want 3", next.calls)
	}
	_, _ = f.Analyze(ctx, "b", balanced)
	if next.calls != 4 {
		t.Fatalf("evicted text was not re-analyzed: calls = %d", next.calls)
	}
}

func TestCallersCannotChangeCachedEntities(t *testing.T) {
	f := New(&countingFilter{}, 10)
	got, _ := f.Analyze(context.Background(), "Alice", balanced)
	got[0].Type = "CHANGED"
	again, _ := f.Analyze(context.Background(), "Alice", balanced)
	if again[0].Type != "PERSON" {
		t.Fatalf("cached entity was changed through a returned slice: %v", again)
	}
}
