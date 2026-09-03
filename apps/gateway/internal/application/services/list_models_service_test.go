package services

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

type countingCatalog struct {
	models       []domain.PublicModel
	routers      []domain.RouterEntry
	modelsCalls  atomic.Int64
	routersCalls atomic.Int64
	err          error
}

func (c *countingCatalog) ListPublicModels(_ context.Context) ([]domain.PublicModel, error) {
	c.modelsCalls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return c.models, nil
}

func (c *countingCatalog) GetPublicModel(_ context.Context, _ string) (domain.PublicModel, error) {
	return domain.PublicModel{}, nil
}

func (c *countingCatalog) ListRouters(_ context.Context) ([]domain.RouterEntry, error) {
	c.routersCalls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return c.routers, nil
}

func (c *countingCatalog) GetRouter(_ context.Context, _ string) (domain.RouterEntry, error) {
	return domain.RouterEntry{}, nil
}

type countingRateProvider struct {
	calls atomic.Int64
	rate  float64
}

func (p *countingRateProvider) EURToUSD(context.Context) (float64, error) {
	p.calls.Add(1)
	if p.rate <= 0 {
		return defaultEURToUSDRate, nil
	}
	return p.rate, nil
}

func TestListModelsService_CachesResults(t *testing.T) {
	catalog := &countingCatalog{
		models: []domain.PublicModel{{PublicModelID: "openai/gpt-4.1-mini"}},
	}
	svc := NewListModelsService(catalog, stubLogger{})

	for i := 0; i < 10; i++ {
		if _, err := svc.Execute(context.Background()); err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
	}

	if got := catalog.modelsCalls.Load(); got != 1 {
		t.Errorf("ListPublicModels called %d times, want 1", got)
	}
	if got := catalog.routersCalls.Load(); got != 1 {
		t.Errorf("ListRouters called %d times, want 1", got)
	}
}

func TestListModelsService_CachesEURToUSDRateWithCatalog(t *testing.T) {
	catalog := &countingCatalog{
		models: []domain.PublicModel{{PublicModelID: "openai/gpt-4.1-mini"}},
	}
	rates := &countingRateProvider{rate: 1.12}
	svc := NewListModelsServiceWithRateProvider(catalog, stubLogger{}, rates, defaultEURToUSDRate)

	now := time.Unix(1000, 0)
	svc.now = func() time.Time { return now }

	first, err := svc.Execute(context.Background())
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	for i := 0; i < 10; i++ {
		entries, err := svc.Execute(context.Background())
		if err != nil {
			t.Fatalf("cached Execute: %v", err)
		}
		if entries[0].EURToUSDRate != 1.12 {
			t.Fatalf("cached rate = %v, want 1.12", entries[0].EURToUSDRate)
		}
	}

	if first[0].EURToUSDRate != 1.12 {
		t.Fatalf("rate = %v, want 1.12", first[0].EURToUSDRate)
	}
	if got := rates.calls.Load(); got != 1 {
		t.Fatalf("EURToUSD called %d times before TTL, want 1", got)
	}

	rates.rate = 1.2
	now = now.Add(CatalogCacheTTL + time.Second)
	entries, err := svc.Execute(context.Background())
	if err != nil {
		t.Fatalf("post-TTL Execute: %v", err)
	}
	if entries[0].EURToUSDRate != 1.2 {
		t.Fatalf("refreshed rate = %v, want 1.2", entries[0].EURToUSDRate)
	}
	if got := rates.calls.Load(); got != 2 {
		t.Fatalf("EURToUSD called %d times after TTL refresh, want 2", got)
	}
}

func TestListModelsService_RefreshesAfterTTL(t *testing.T) {
	catalog := &countingCatalog{
		models: []domain.PublicModel{{PublicModelID: "openai/gpt-4.1-mini"}},
	}
	svc := NewListModelsService(catalog, stubLogger{})

	now := time.Unix(1000, 0)
	svc.now = func() time.Time { return now }

	if _, err := svc.Execute(context.Background()); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if _, err := svc.Execute(context.Background()); err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if got := catalog.modelsCalls.Load(); got != 1 {
		t.Fatalf("before TTL: ListPublicModels called %d times, want 1", got)
	}

	now = now.Add(CatalogCacheTTL + time.Second)
	if _, err := svc.Execute(context.Background()); err != nil {
		t.Fatalf("post-TTL Execute: %v", err)
	}
	if got := catalog.modelsCalls.Load(); got != 2 {
		t.Errorf("after TTL: ListPublicModels called %d times, want 2", got)
	}
}

func TestListModelsService_CoalescesConcurrentMisses(t *testing.T) {
	catalog := &countingCatalog{
		models: []domain.PublicModel{{PublicModelID: "openai/gpt-4.1-mini"}},
	}
	svc := NewListModelsService(catalog, stubLogger{})

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := svc.Execute(context.Background()); err != nil {
				t.Errorf("Execute: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := catalog.modelsCalls.Load(); got != 1 {
		t.Errorf("ListPublicModels called %d times under concurrency, want 1", got)
	}
}

func TestListModelsService_DoesNotCacheErrors(t *testing.T) {
	catalog := &countingCatalog{err: errors.New("db down")}
	svc := NewListModelsService(catalog, stubLogger{})

	if _, err := svc.Execute(context.Background()); err == nil {
		t.Fatal("expected error on first call")
	}

	catalog.err = nil
	catalog.models = []domain.PublicModel{{PublicModelID: "openai/gpt-4.1-mini"}}

	if _, err := svc.Execute(context.Background()); err != nil {
		t.Fatalf("expected success after recovery, got: %v", err)
	}
	if got := catalog.modelsCalls.Load(); got != 2 {
		t.Errorf("ListPublicModels called %d times, want 2 (errors must not cache)", got)
	}
}
