package services

import (
	"context"
	"sync"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/ports"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"golang.org/x/sync/singleflight"
)

// CatalogCacheTTL is how long /v1/models results are cached in memory.
// The catalog changes only when admins edit models/routers, so a short TTL
// is invisible to users and collapses request floods into one DB hit per window.
const CatalogCacheTTL = 60 * time.Second

const defaultEURToUSDRate = 1.08

type EURToUSDRateProvider interface {
	EURToUSD(ctx context.Context) (float64, error)
}

type fixedEURToUSDRate float64

func (r fixedEURToUSDRate) EURToUSD(context.Context) (float64, error) {
	return float64(r), nil
}

// ListModelsService handles the GET /v1/models flow.
//
// The endpoint is intentionally public (no auth) so the landing page and
// other unauthenticated clients can render the live model catalog. To
// protect the database from request floods, results are cached in memory
// for CatalogCacheTTL and concurrent misses are coalesced via singleflight.
type ListModelsService struct {
	catalog  ports.ModelCatalog
	logger   ports.Logger
	eurToUSD EURToUSDRateProvider
	fallback float64
	ttl      time.Duration
	now      func() time.Time

	mu        sync.RWMutex
	cached    []domain.ModelCatalogEntry
	expiresAt time.Time

	sf singleflight.Group
}

func NewListModelsService(catalog ports.ModelCatalog, logger ports.Logger) *ListModelsService {
	return NewListModelsServiceWithRateProvider(catalog, logger, fixedEURToUSDRate(defaultEURToUSDRate), defaultEURToUSDRate)
}

func NewListModelsServiceWithRateProvider(catalog ports.ModelCatalog, logger ports.Logger, eurToUSD EURToUSDRateProvider, fallback float64) *ListModelsService {
	if eurToUSD == nil {
		eurToUSD = fixedEURToUSDRate(defaultEURToUSDRate)
	}
	if fallback <= 0 {
		fallback = defaultEURToUSDRate
	}
	return &ListModelsService{
		catalog:  catalog,
		logger:   logger,
		eurToUSD: eurToUSD,
		fallback: fallback,
		ttl:      CatalogCacheTTL,
		now:      time.Now,
	}
}

func (s *ListModelsService) Execute(ctx context.Context) ([]domain.ModelCatalogEntry, error) {
	if entries, ok := s.fromCache(); ok {
		return entries, nil
	}

	v, err, _ := s.sf.Do("list", func() (any, error) {
		if entries, ok := s.fromCache(); ok {
			return entries, nil
		}
		return s.refresh(ctx)
	})
	if err != nil {
		return nil, err
	}
	return v.([]domain.ModelCatalogEntry), nil
}

func (s *ListModelsService) fromCache() ([]domain.ModelCatalogEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cached != nil && s.now().Before(s.expiresAt) {
		return s.cached, true
	}
	return nil, false
}

func (s *ListModelsService) refresh(ctx context.Context) ([]domain.ModelCatalogEntry, error) {
	models, err := s.catalog.ListPublicModels(ctx)
	if err != nil {
		s.logger.Error("failed to list models", "error", err)
		return nil, domain.ErrInternal("an internal error occurred")
	}

	routers, err := s.catalog.ListRouters(ctx)
	if err != nil {
		s.logger.Error("failed to list routers", "error", err)
		return nil, domain.ErrInternal("an internal error occurred")
	}

	eurToUSDRate := s.fallback
	if rate, err := s.eurToUSD.EURToUSD(ctx); err == nil && rate > 0 {
		eurToUSDRate = rate
	} else if err != nil {
		s.logger.Warn("failed to fetch EUR->USD rate; using fallback", "error", err)
	}
	entries := make([]domain.ModelCatalogEntry, 0, len(models)+len(routers))
	for i := range models {
		entries = append(entries, domain.ModelCatalogEntry{
			Kind:         domain.CatalogKindPublicModel,
			PublicModel:  &models[i],
			EURToUSDRate: eurToUSDRate,
		})
	}
	for i := range routers {
		entries = append(entries, domain.ModelCatalogEntry{
			Kind:         domain.CatalogKindRouter,
			Router:       &routers[i],
			EURToUSDRate: eurToUSDRate,
		})
	}

	s.mu.Lock()
	s.cached = entries
	s.expiresAt = s.now().Add(s.ttl)
	s.mu.Unlock()

	return entries, nil
}
