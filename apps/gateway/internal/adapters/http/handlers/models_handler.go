package handlers

import (
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
	return dto.ModelData{
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
		Features:                        modelFeatures(m),
		Endpoints:                       modelEndpoints(m),
		ProofMode:                       proofMode,
		ProofsEnabled:                   domain.ProofModeEnabled(proofMode),
	}
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
		Features:    []string{"routing"},
		Endpoints:   []string{"chat/completions"},
		ProofMode:   domain.ProofModeNone,
	}
}

func modelFeatures(m domain.PublicModel) []string {
	features := make([]string, 0, 5)
	if m.SupportsChatCompletionsStream {
		features = append(features, "streaming")
	}
	if m.SupportsTools {
		features = append(features, "function-calling")
	}
	if m.SupportsParallelToolCalls {
		features = append(features, "parallel-tool-calls")
	}
	if m.SupportsStructuredOutput {
		features = append(features, "structured-outputs")
	}
	if m.SupportsReasoning {
		features = append(features, "reasoning")
	}
	switch m.EffectiveProofMode() {
	case domain.ProofModeTinfoilAttestedTransport:
		features = append(features, "tinfoil-attested-transport")
	}
	return features
}

func modelEndpoints(m domain.PublicModel) []string {
	endpoints := make([]string, 0, 1)
	if m.SupportsChatCompletions {
		endpoints = append(endpoints, "chat/completions")
	}
	return endpoints
}

func eurToCents(price decimal.Decimal) int64 {
	return price.Mul(decimal.NewFromInt(100)).Round(0).IntPart()
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
	usd, _ := price.Mul(decimal.NewFromFloat(factor)).Round(4).Float64()
	return &usd
}

func privateBaseModel(publicModelID, upstreamModelName string) *string {
	baseModel := strings.TrimSpace(upstreamModelName)
	if !strings.HasPrefix(publicModelID, "private/") || baseModel == "" {
		return nil
	}
	return &baseModel
}
