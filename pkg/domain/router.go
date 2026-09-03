package domain

import (
	"encoding/json"
	"time"
)

// CatalogKind identifies the kind of user-facing model catalog entry.
type CatalogKind string

const (
	CatalogKindPublicModel CatalogKind = "public_model"
	CatalogKindRouter      CatalogKind = "router"
)

// RouterStrategyType identifies how a router chooses a concrete public model.
type RouterStrategyType string

const (
	RouterStrategyFallback  RouterStrategyType = "fallback"
	RouterStrategyEmbedding RouterStrategyType = "embedding"
)

// RouteRequest is the gateway-to-router request contract.
//
// The gateway passes the full canonical generation request along with the
// router_id.  The router service is a black box: it loads its strategy from
// the DB and extracts whatever features that strategy needs from the request
// internally.  When a new strategy needs new features, only the router
// service changes — the gateway stays untouched.
type RouteRequest struct {
	RouterID string
	Request  GenerateRequest
}

// RouteDecision is the router service output contract.
type RouteDecision struct {
	PublicModelID  string
	Category       *string
	Score          *float32
	CategoryScores []RoutingCategoryScore
	FallbackUsed   bool
	Reason         string
}

// RoutingCategoryScore is the per-category embedding score snapshot returned
// by the router and persisted with routed usage events.
type RoutingCategoryScore struct {
	Category        string  `json:"category"`
	Score           float32 `json:"score"`
	Threshold       float32 `json:"threshold"`
	PassedThreshold bool    `json:"passed_threshold"`
	Selected        bool    `json:"selected"`
}

// RouterEntry is a user-facing router configuration managed by the control plane.
type RouterEntry struct {
	ID                    string
	RouterID              string
	DisplayName           string
	Description           *string
	FallbackPublicModelID string
	StrategyType          RouterStrategyType
	StrategyConfig        json.RawMessage
	Active                bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ModelCatalogEntry is returned by catalog APIs that mix concrete public models and routers.
type ModelCatalogEntry struct {
	Kind         CatalogKind
	PublicModel  *PublicModel
	Router       *RouterEntry
	EURToUSDRate float64
}

// RouterCategory is a strategy-specific routing target managed by the router
// service.  The control plane treats categories as opaque metadata it can
// proxy on behalf of the admin UI.
type RouterCategory struct {
	ID            string
	RouterID      string
	Name          string
	PublicModelID string
	Threshold     float32
}

// RouterCategoryInput is the create/update payload for a router category.
type RouterCategoryInput struct {
	Name          string
	PublicModelID string
	Threshold     float32
}
