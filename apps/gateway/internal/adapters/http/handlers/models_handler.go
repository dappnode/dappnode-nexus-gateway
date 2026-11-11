package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/adapters/http/dto"
	"github.com/dappnode/dappnode-nexus-gateway/apps/gateway/internal/application/services"
	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/shopspring/decimal"
)

// ModelsHandler handles GET /v1/models.
type ModelsHandler struct {
	service *services.ListModelsService
}

func NewModelsHandler(service *services.ListModelsService) *ModelsHandler {
	return &ModelsHandler{service: service}
}

func (h *ModelsHandler) Handle(w http.ResponseWriter, r *http.Request) {
	models, err := h.service.Execute(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}

	data := make([]dto.ModelData, 0, len(models))
	for _, entry := range models {
		data = append(data, toModelData(entry))
	}

	w.Header().Set("Cache-Control", "public, max-age=60")
	WriteJSON(w, http.StatusOK, dto.ModelsResponse{
		Object: "list",
		Data:   data,
	})
}

func toModelData(entry domain.ModelCatalogEntry) dto.ModelData {
	if entry.Kind == domain.CatalogKindRouter && entry.Router != nil {
		return toRouterData(*entry.Router)
	}
	if entry.PublicModel == nil {
		return dto.ModelData{}
	}
	return toPublicModelData(*entry.PublicModel, entry.EURToUSDRate)
}

func toPublicModelData(m domain.PublicModel, eurToUSDRate float64) dto.ModelData {
	inputPrice := eurToCents(m.InputPricePerMillion)
	outputPrice := eurToCents(m.OutputPricePerMillion)
	proofMode := m.EffectiveProofMode()
	data := dto.ModelData{
		ID:                              m.PublicModelID,
		Object:                          "model",
		Kind:                            string(domain.CatalogKindPublicModel),
		BaseModel:                       privateBaseModel(m.PublicModelID, m.UpstreamModelName),
		OwnedBy:                         "nexus",
		Created:                         0,
		DisplayName:                     m.DisplayName,
		Description:                     m.Description,
		ContextSize:                     &m.MaxContextWindow,
		MaxOutputTokens:                 &m.MaxOutputTokens,
		Currency:                        &m.Currency,
		InputPricePer1MTokensCents:      &inputPrice,
		OutputPricePer1MTokensCents:     &outputPrice,
		CacheReadPricePer1MTokensCents:  eurPtrToCentsPtr(m.CacheReadPricePerMillion),
		CacheWritePricePer1MTokensCents: eurPtrToCentsPtr(m.CacheWritePricePerMillion),
		InputPricePer1MTokensUSD:        priceToUSDPtr(m.Currency, m.InputPricePerMillion, eurToUSDRate),
		OutputPricePer1MTokensUSD:       priceToUSDPtr(m.Currency, m.OutputPricePerMillion, eurToUSDRate),
		CacheReadPricePer1MTokensUSD:    pricePtrToUSDPtr(m.Currency, m.CacheReadPricePerMillion, eurToUSDRate),
		CacheWritePricePer1MTokensUSD:   pricePtrToUSDPtr(m.Currency, m.CacheWritePricePerMillion, eurToUSDRate),
		PricePerImageCents:              fractionalCents(m.PricePerImage),
		ImageInputPricePer1MTokensCents: eurPtrToCentsPtr(m.ImageInputPricePerMillion),
		ImageInputPricePer1MTokensUSD:   pricePtrToUSDPtr(m.Currency, m.ImageInputPricePerMillion, eurToUSDRate),
		ImageMaxCostPerImageCents:       fractionalCents(m.ImageMaxCostPerImage),
		PricePerImageUSD:                pricePtrToUSDPtr(m.Currency, m.PricePerImage, eurToUSDRate),
		Capabilities:                    publishedCapabilities(m),
		Endpoints:                       modelEndpoints(m),
		ProofMode:                       proofMode,
		ProofsEnabled:                   domain.ProofModeEnabled(proofMode),
	}
	if !m.SupportsChatCompletions && m.SupportsEndpoint(domain.EndpointImageGenerations) {
		// Image requests have no chat token limits; token-priced image models
		// still publish their retail text/reference/output rates.
		data.ContextSize, data.MaxOutputTokens = nil, nil
		if m.ImageInputPricePerMillion == nil {
			data.InputPricePer1MTokensCents, data.OutputPricePer1MTokensCents = nil, nil
			data.InputPricePer1MTokensUSD, data.OutputPricePer1MTokensUSD = nil, nil
		}
	}
	return data
}

func toRouterData(r domain.RouterEntry) dto.ModelData {
	return dto.ModelData{
		ID:          r.RouterID,
		Object:      "model",
		Kind:        string(domain.CatalogKindRouter),
		OwnedBy:     "nexus",
		Created:     r.CreatedAt.Unix(),
		DisplayName: r.DisplayName,
		Description: r.Description,
		Endpoints:   []string{"chat/completions"},
		ProofMode:   domain.ProofModeNone,
	}
}

// publishedCapabilities is the operator's record, unchanged, or nothing when
// the operator stated nothing.
func publishedCapabilities(m domain.PublicModel) json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(m.Capabilities, &object) != nil || len(object) == 0 {
		return nil
	}
	return m.Capabilities
}

func modelEndpoints(m domain.PublicModel) []string {
	endpoints := make([]string, 0, 1)
	if m.SupportsEndpoint(domain.EndpointSystemOne) {
		return []string{"systemone"}
	}
	if m.SupportsChatCompletions {
		endpoints = append(endpoints, "chat/completions")
	}
	if m.SupportsEndpoint(domain.EndpointImageGenerations) {
		endpoints = append(endpoints, "images/generations")
		if m.SupportsImageEdits() {
			endpoints = append(endpoints, "images/edits")
		}
	}
	return endpoints
}

func eurToCents(price decimal.Decimal) int64 {
	return price.Mul(decimal.NewFromInt(100)).Round(0).IntPart()
}

// fractionalCents is a price in cents to microcent precision: per-image prices are
// often fractions of a cent.
func fractionalCents(price *decimal.Decimal) *float64 {
	if price == nil {
		return nil
	}
	cents, _ := price.Mul(decimal.NewFromInt(100)).Round(6).Float64()
	return &cents
}

func eurPtrToCentsPtr(price *decimal.Decimal) *int64 {
	if price == nil {
		return nil
	}
	v := eurToCents(*price)
	return &v
}

func pricePtrToUSDPtr(currency string, price *decimal.Decimal, eurToUSDRate float64) *float64 {
	if price == nil {
		return nil
	}
	return priceToUSDPtr(currency, *price, eurToUSDRate)
}

func priceToUSDPtr(currency string, price decimal.Decimal, eurToUSDRate float64) *float64 {
	factor := 0.0
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "USD":
		factor = 1
	case "EUR":
		factor = eurToUSDRate
	}
	if factor <= 0 {
		return nil
	}
	usd, _ := price.Mul(decimal.NewFromFloat(factor)).Round(8).Float64()
	return &usd
}

func privateBaseModel(publicModelID, upstreamModelName string) *string {
	baseModel := strings.TrimSpace(upstreamModelName)
	if !strings.HasPrefix(publicModelID, "private/") || baseModel == "" {
		return nil
	}
	return &baseModel
}
