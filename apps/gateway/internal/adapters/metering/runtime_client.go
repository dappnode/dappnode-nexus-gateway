package metering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/shopspring/decimal"
)

const microcentsPerCurrencyUnit = int64(100_000_000)

func (c *Client) AuthenticateAPIKey(ctx context.Context, rawKey string) (domain.AuthContext, error) {
	if rawKey == "" {
		return domain.AuthContext{}, domain.ErrInvalidAPIKey("missing API key")
	}
	hash := sha256.Sum256([]byte(rawKey))
	payload := authenticateRequest{KeyHash: hex.EncodeToString(hash[:])}
	var response authenticateResponse
	if err := c.postJSON(ctx, "/internal/gateway/authenticate", payload, &response, 2); err != nil {
		return domain.AuthContext{}, err
	}
	if err := validateAuthenticateResponse(response); err != nil {
		return domain.AuthContext{}, err
	}
	return domain.AuthContext{
		Account: domain.Account{
			ID:                 response.Account.ID,
			ExternalCustomerID: response.Account.ExternalCustomerID,
			Status:             response.Account.Status,
		},
		APIKey: domain.APIKey{
			ID:         response.APIKey.ID,
			AccountID:  response.APIKey.AccountID,
			Name:       response.APIKey.Name,
			KeyPrefix:  response.APIKey.KeyPrefix,
			Active:     response.APIKey.Active,
			PIIMode:    response.APIKey.PIIMode,
			LastUsedAt: response.APIKey.LastUsedAt,
		},
	}, nil
}

func (c *Client) ListPublicModels(ctx context.Context) ([]domain.PublicModel, error) {
	var response []runtimePublicModel
	if err := c.getJSON(ctx, "/internal/gateway/models", &response, 2); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, runtimeContractError("model list", "response must be a JSON array")
	}
	models := make([]domain.PublicModel, 0, len(response))
	seen := make(map[string]struct{}, len(response))
	for i, model := range response {
		if err := validateRuntimePublicModel(model, ""); err != nil {
			return nil, err.WithMeta("item_index", i)
		}
		if _, ok := seen[model.PublicModelID]; ok {
			return nil, runtimeContractError("model list", "duplicate public_model_id").WithMeta("public_model_id", model.PublicModelID)
		}
		seen[model.PublicModelID] = struct{}{}
		models = append(models, model.toDomain())
	}
	return models, nil
}

func (c *Client) GetPublicModel(ctx context.Context, publicModelID string) (domain.PublicModel, error) {
	var response runtimePublicModel
	path := "/internal/gateway/models/" + url.PathEscape(publicModelID)
	if err := c.getJSON(ctx, path, &response, 2); err != nil {
		return domain.PublicModel{}, err
	}
	if err := validateRuntimePublicModel(response, publicModelID); err != nil {
		return domain.PublicModel{}, err
	}
	return response.toDomain(), nil
}

func (c *Client) ListRouters(ctx context.Context) ([]domain.RouterEntry, error) {
	var response []runtimeRouter
	if err := c.getJSON(ctx, "/internal/gateway/routers", &response, 2); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, runtimeContractError("router list", "response must be a JSON array")
	}
	routers := make([]domain.RouterEntry, 0, len(response))
	seen := make(map[string]struct{}, len(response))
	for i, router := range response {
		if err := validateRuntimeRouter(router, ""); err != nil {
			return nil, err.WithMeta("item_index", i)
		}
		if _, ok := seen[router.RouterID]; ok {
			return nil, runtimeContractError("router list", "duplicate router_id").WithMeta("router_id", router.RouterID)
		}
		seen[router.RouterID] = struct{}{}
		routers = append(routers, router.toDomain())
	}
	return routers, nil
}

func (c *Client) GetRouter(ctx context.Context, routerID string) (domain.RouterEntry, error) {
	var response runtimeRouter
	path := "/internal/gateway/routers/" + url.PathEscape(routerID)
	if err := c.getJSON(ctx, path, &response, 2); err != nil {
		return domain.RouterEntry{}, err
	}
	if err := validateRuntimeRouter(response, routerID); err != nil {
		return domain.RouterEntry{}, err
	}
	return response.toDomain(), nil
}

func (c *Client) UpsertTinfoilTransportProof(ctx context.Context, proof domain.TinfoilTransportProof) error {
	return c.postJSON(ctx, "/internal/gateway/tinfoil-proofs", runtimeProofFromDomain(proof), nil, 2)
}

func (c *Client) GetTinfoilTransportProof(ctx context.Context, accountID, providerResponseID string) (domain.TinfoilTransportProof, error) {
	query := url.Values{}
	query.Set("account_id", accountID)
	query.Set("provider_response_id", providerResponseID)
	var response runtimeProof
	if err := c.getJSON(ctx, "/internal/gateway/tinfoil-proofs?"+query.Encode(), &response, 2); err != nil {
		return domain.TinfoilTransportProof{}, err
	}
	if err := validateRuntimeProof(response, accountID, providerResponseID); err != nil {
		return domain.TinfoilTransportProof{}, err
	}
	return response.toDomain(), nil
}

func validateAuthenticateResponse(response authenticateResponse) *domain.GatewayError {
	switch {
	case strings.TrimSpace(response.Account.ID) == "":
		return runtimeContractError("authentication", "account.id is required")
	case strings.TrimSpace(response.APIKey.ID) == "":
		return runtimeContractError("authentication", "api_key.id is required")
	case strings.TrimSpace(response.APIKey.AccountID) == "":
		return runtimeContractError("authentication", "api_key.account_id is required")
	case response.APIKey.AccountID != response.Account.ID:
		return runtimeContractError("authentication", "api key does not belong to returned account")
	case response.Account.Status != domain.AccountStatusActive:
		return runtimeContractError("authentication", "successful response contains an inactive account")
	case !response.APIKey.Active:
		return runtimeContractError("authentication", "successful response contains an inactive API key")
	case response.APIKey.PIIMode == "":
		return runtimeContractError("authentication", "api_key.pii_mode is required")
	default:
		if _, ok := domain.NormalizeAPIKeyPIIMode(response.APIKey.PIIMode); !ok {
			return runtimeContractError("authentication", "api_key.pii_mode is invalid")
		}
	}
	return nil
}

func validateRuntimePublicModel(model runtimePublicModel, expectedPublicModelID string) *domain.GatewayError {
	switch {
	case strings.TrimSpace(model.ID) == "":
		return runtimeContractError("public model", "id is required")
	case strings.TrimSpace(model.PublicModelID) == "":
		return runtimeContractError("public model", "public_model_id is required")
	case expectedPublicModelID != "" && model.PublicModelID != expectedPublicModelID:
		return runtimeContractError("public model", "returned public_model_id does not match request").WithMeta(
			"expected_public_model_id", expectedPublicModelID,
			"actual_public_model_id", model.PublicModelID,
		)
	case strings.TrimSpace(model.DisplayName) == "":
		return runtimeContractError("public model", "display_name is required")
	case strings.TrimSpace(model.ProviderModelID) == "":
		return runtimeContractError("public model", "provider_model_id is required")
	case strings.TrimSpace(model.UpstreamModelName) == "":
		return runtimeContractError("public model", "upstream_model_name is required")
	case strings.TrimSpace(model.ProviderConfig.ID) == "":
		return runtimeContractError("public model", "provider_config.id is required")
	case strings.TrimSpace(model.ProviderConfig.ProviderName) == "":
		return runtimeContractError("public model", "provider_config.provider_name is required")
	case strings.TrimSpace(model.ProviderConfig.BaseURL) == "":
		return runtimeContractError("public model", "provider_config.base_url is required")
	case strings.TrimSpace(model.ProviderConfig.APIKeySecretRef) == "":
		return runtimeContractError("public model", "provider_config.api_key_secret_ref is required")
	case !model.Active:
		return runtimeContractError("public model", "inactive model returned by active catalog query")
	case !model.ProviderConfig.Active:
		return runtimeContractError("public model", "inactive provider config returned by active catalog query")
	case model.SupportsChatCompletions == nil:
		return runtimeContractError("public model", "supports_chat_completions is required")
	case model.SupportsChatCompletionsStream == nil:
		return runtimeContractError("public model", "supports_chat_completions_stream is required")
	case model.SupportsTools == nil:
		return runtimeContractError("public model", "supports_tools is required")
	case model.SupportsParallelToolCalls == nil:
		return runtimeContractError("public model", "supports_parallel_tool_calls is required")
	case model.SupportsStructuredOutput == nil:
		return runtimeContractError("public model", "supports_structured_output is required")
	case model.SupportsReasoning == nil:
		return runtimeContractError("public model", "supports_reasoning is required")
	case model.ProofMode != domain.ProofModeNone && model.ProofMode != domain.ProofModeTinfoilAttestedTransport:
		return runtimeContractError("public model", "proof_mode is invalid")
	case model.MaxContextWindow <= 0:
		return runtimeContractError("public model", "max_context_window must be positive")
	case model.MaxOutputTokens <= 0:
		return runtimeContractError("public model", "max_output_tokens must be positive")
	case model.InputPricePer1MTokensMicrocents == nil:
		return runtimeContractError("public model", "input price is required")
	case *model.InputPricePer1MTokensMicrocents < 0:
		return runtimeContractError("public model", "input price must not be negative")
	case model.OutputPricePer1MTokensMicrocents == nil:
		return runtimeContractError("public model", "output price is required")
	case *model.OutputPricePer1MTokensMicrocents < 0:
		return runtimeContractError("public model", "output price must not be negative")
	case model.CacheReadPricePer1MTokensMicrocents != nil && *model.CacheReadPricePer1MTokensMicrocents < 0:
		return runtimeContractError("public model", "cache read price must not be negative")
	case model.CacheWritePricePer1MTokensMicrocents != nil && *model.CacheWritePricePer1MTokensMicrocents < 0:
		return runtimeContractError("public model", "cache write price must not be negative")
	case strings.TrimSpace(model.Currency) == "":
		return runtimeContractError("public model", "currency is required")
	}
	if model.Fallback != nil {
		if err := validateRuntimeProviderTarget(*model.Fallback); err != nil {
			return err
		}
		if model.Fallback.ProviderModelID == model.ProviderModelID &&
			model.Fallback.ProviderConfig.ID == model.ProviderConfig.ID {
			return runtimeContractError("public model", "fallback must differ from primary target")
		}
	}
	return nil
}

func validateRuntimeProviderTarget(target runtimeProviderTarget) *domain.GatewayError {
	switch {
	case strings.TrimSpace(target.ProviderModelID) == "":
		return runtimeContractError("public model fallback", "provider_model_id is required")
	case strings.TrimSpace(target.UpstreamModelName) == "":
		return runtimeContractError("public model fallback", "upstream_model_name is required")
	case strings.TrimSpace(target.ProviderConfig.ID) == "":
		return runtimeContractError("public model fallback", "provider_config.id is required")
	case strings.TrimSpace(target.ProviderConfig.ProviderName) == "":
		return runtimeContractError("public model fallback", "provider_config.provider_name is required")
	case strings.TrimSpace(target.ProviderConfig.BaseURL) == "":
		return runtimeContractError("public model fallback", "provider_config.base_url is required")
	case strings.TrimSpace(target.ProviderConfig.APIKeySecretRef) == "":
		return runtimeContractError("public model fallback", "provider_config.api_key_secret_ref is required")
	case !target.ProviderConfig.Active:
		return runtimeContractError("public model fallback", "provider config is inactive")
	default:
		return nil
	}
}

func validateRuntimeRouter(router runtimeRouter, expectedRouterID string) *domain.GatewayError {
	switch {
	case strings.TrimSpace(router.ID) == "":
		return runtimeContractError("router", "id is required")
	case strings.TrimSpace(router.RouterID) == "":
		return runtimeContractError("router", "router_id is required")
	case expectedRouterID != "" && router.RouterID != expectedRouterID:
		return runtimeContractError("router", "returned router_id does not match request").WithMeta(
			"expected_router_id", expectedRouterID,
			"actual_router_id", router.RouterID,
		)
	case strings.TrimSpace(router.DisplayName) == "":
		return runtimeContractError("router", "display_name is required")
	case strings.TrimSpace(router.FallbackPublicModelID) == "":
		return runtimeContractError("router", "fallback_public_model_id is required")
	case strings.TrimSpace(router.StrategyType) == "":
		return runtimeContractError("router", "strategy_type is required")
	case !jsonObject(router.StrategyConfig):
		return runtimeContractError("router", "strategy_config must be a JSON object")
	case !router.Active:
		return runtimeContractError("router", "inactive router returned by active catalog query")
	case router.CreatedAt.IsZero():
		return runtimeContractError("router", "created_at is required")
	case router.UpdatedAt.IsZero():
		return runtimeContractError("router", "updated_at is required")
	}
	return nil
}

func validateRuntimeProof(proof runtimeProof, expectedAccountID, expectedProviderResponseID string) *domain.GatewayError {
	switch {
	case strings.TrimSpace(proof.ID) == "":
		return runtimeContractError("Tinfoil proof", "id is required")
	case strings.TrimSpace(proof.AccountID) == "":
		return runtimeContractError("Tinfoil proof", "account_id is required")
	case proof.AccountID != expectedAccountID:
		return runtimeContractError("Tinfoil proof", "returned account_id does not match request")
	case strings.TrimSpace(proof.APIKeyID) == "":
		return runtimeContractError("Tinfoil proof", "api_key_id is required")
	case strings.TrimSpace(proof.Provider) == "":
		return runtimeContractError("Tinfoil proof", "provider is required")
	case strings.TrimSpace(proof.PublicModelID) == "":
		return runtimeContractError("Tinfoil proof", "public_model_id is required")
	case strings.TrimSpace(proof.UpstreamModelID) == "":
		return runtimeContractError("Tinfoil proof", "upstream_model_id is required")
	case strings.TrimSpace(proof.ProviderResponseID) == "":
		return runtimeContractError("Tinfoil proof", "provider_response_id is required")
	case proof.ProviderResponseID != expectedProviderResponseID:
		return runtimeContractError("Tinfoil proof", "returned provider_response_id does not match request")
	case strings.TrimSpace(proof.Status) == "":
		return runtimeContractError("Tinfoil proof", "status is required")
	case proof.CreatedAt.IsZero():
		return runtimeContractError("Tinfoil proof", "created_at is required")
	}
	return nil
}

func runtimeContractError(contract, reason string) *domain.GatewayError {
	return meteringInternalError("contract", contract, "reason", reason)
}

func jsonObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func (m runtimePublicModel) toDomain() domain.PublicModel {
	model := domain.PublicModel{
		ID:                            m.ID,
		PublicModelID:                 m.PublicModelID,
		DisplayName:                   m.DisplayName,
		Description:                   m.Description,
		ProviderModelID:               m.ProviderModelID,
		UpstreamModelName:             m.UpstreamModelName,
		ProviderConfig:                m.ProviderConfig.toDomain(),
		SupportsChatCompletions:       *m.SupportsChatCompletions,
		SupportsChatCompletionsStream: *m.SupportsChatCompletionsStream,
		SupportsTools:                 *m.SupportsTools,
		SupportsParallelToolCalls:     *m.SupportsParallelToolCalls,
		SupportsStructuredOutput:      *m.SupportsStructuredOutput,
		SupportsReasoning:             *m.SupportsReasoning,
		ProofMode:                     m.ProofMode,
		MaxContextWindow:              m.MaxContextWindow,
		MaxOutputTokens:               m.MaxOutputTokens,
		InputPricePerMillion:          priceFromMicrocents(*m.InputPricePer1MTokensMicrocents),
		OutputPricePerMillion:         priceFromMicrocents(*m.OutputPricePer1MTokensMicrocents),
		Currency:                      m.Currency,
		Active:                        m.Active,
	}
	if m.Fallback != nil {
		model.Fallback = &domain.ProviderTarget{
			ProviderModelID:   m.Fallback.ProviderModelID,
			UpstreamModelName: m.Fallback.UpstreamModelName,
			ProviderConfig:    m.Fallback.ProviderConfig.toDomain(),
		}
	}
	if model.ProofMode == "" {
		model.ProofMode = domain.ProofModeNone
	}
	if m.CacheReadPricePer1MTokensMicrocents != nil {
		price := priceFromMicrocents(*m.CacheReadPricePer1MTokensMicrocents)
		model.CacheReadPricePerMillion = &price
	}
	if m.CacheWritePricePer1MTokensMicrocents != nil {
		price := priceFromMicrocents(*m.CacheWritePricePer1MTokensMicrocents)
		model.CacheWritePricePerMillion = &price
	}
	return model
}

func (p runtimeProviderConfig) toDomain() domain.ProviderConfig {
	return domain.ProviderConfig{
		ID: p.ID, ProviderName: p.ProviderName, BaseURL: p.BaseURL,
		APIKeySecretRef: p.APIKeySecretRef, OrganizationRef: p.OrganizationRef, Active: p.Active,
	}
}

func (r runtimeRouter) toDomain() domain.RouterEntry {
	return domain.RouterEntry{
		ID: r.ID, RouterID: r.RouterID, DisplayName: r.DisplayName, Description: r.Description,
		FallbackPublicModelID: r.FallbackPublicModelID,
		StrategyType:          domain.RouterStrategyType(r.StrategyType), StrategyConfig: r.StrategyConfig,
		Active: r.Active, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func priceFromMicrocents(value int64) decimal.Decimal {
	return decimal.NewFromInt(value).Div(decimal.NewFromInt(microcentsPerCurrencyUnit))
}

type authenticateRequest struct {
	KeyHash string `json:"key_hash"`
}

type authenticateResponse struct {
	Account runtimeAccount `json:"account"`
	APIKey  runtimeAPIKey  `json:"api_key"`
}

type runtimeAccount struct {
	ID                 string  `json:"id"`
	ExternalCustomerID *string `json:"external_customer_id,omitempty"`
	Status             string  `json:"status"`
}

type runtimeAPIKey struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"account_id"`
	Name       *string    `json:"name,omitempty"`
	KeyPrefix  string     `json:"key_prefix"`
	Active     bool       `json:"active"`
	PIIMode    string     `json:"pii_mode"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type runtimePublicModel struct {
	ID                                   string                 `json:"id"`
	PublicModelID                        string                 `json:"public_model_id"`
	DisplayName                          string                 `json:"display_name"`
	Description                          *string                `json:"description,omitempty"`
	ProviderModelID                      string                 `json:"provider_model_id"`
	UpstreamModelName                    string                 `json:"upstream_model_name"`
	ProviderConfig                       runtimeProviderConfig  `json:"provider_config"`
	Fallback                             *runtimeProviderTarget `json:"fallback,omitempty"`
	SupportsChatCompletions              *bool                  `json:"supports_chat_completions"`
	SupportsChatCompletionsStream        *bool                  `json:"supports_chat_completions_stream"`
	SupportsTools                        *bool                  `json:"supports_tools"`
	SupportsParallelToolCalls            *bool                  `json:"supports_parallel_tool_calls"`
	SupportsStructuredOutput             *bool                  `json:"supports_structured_output"`
	SupportsReasoning                    *bool                  `json:"supports_reasoning"`
	ProofMode                            string                 `json:"proof_mode"`
	MaxContextWindow                     int                    `json:"max_context_window"`
	MaxOutputTokens                      int                    `json:"max_output_tokens"`
	InputPricePer1MTokensMicrocents      *int64                 `json:"input_price_per_1m_tokens_microcents"`
	OutputPricePer1MTokensMicrocents     *int64                 `json:"output_price_per_1m_tokens_microcents"`
	CacheReadPricePer1MTokensMicrocents  *int64                 `json:"cache_read_price_per_1m_tokens_microcents,omitempty"`
	CacheWritePricePer1MTokensMicrocents *int64                 `json:"cache_write_price_per_1m_tokens_microcents,omitempty"`
	Currency                             string                 `json:"currency"`
	Active                               bool                   `json:"active"`
}

type runtimeProviderTarget struct {
	ProviderModelID   string                `json:"provider_model_id"`
	UpstreamModelName string                `json:"upstream_model_name"`
	ProviderConfig    runtimeProviderConfig `json:"provider_config"`
}

type runtimeProviderConfig struct {
	ID              string  `json:"id"`
	ProviderName    string  `json:"provider_name"`
	BaseURL         string  `json:"base_url"`
	APIKeySecretRef string  `json:"api_key_secret_ref"`
	OrganizationRef *string `json:"organization_ref,omitempty"`
	Active          bool    `json:"active"`
}

type runtimeRouter struct {
	ID                    string          `json:"id"`
	RouterID              string          `json:"router_id"`
	DisplayName           string          `json:"display_name"`
	Description           *string         `json:"description,omitempty"`
	FallbackPublicModelID string          `json:"fallback_public_model_id"`
	StrategyType          string          `json:"strategy_type"`
	StrategyConfig        json.RawMessage `json:"strategy_config"`
	Active                bool            `json:"active"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

type runtimeProof struct {
	ID                       string          `json:"id,omitempty"`
	AccountID                string          `json:"account_id"`
	APIKeyID                 string          `json:"api_key_id"`
	Provider                 string          `json:"provider"`
	PublicModelID            string          `json:"public_model_id"`
	UpstreamModelID          string          `json:"upstream_model_id"`
	ProviderResponseID       string          `json:"provider_response_id"`
	EnclaveHost              *string         `json:"enclave_host,omitempty"`
	ConfigRepo               *string         `json:"config_repo,omitempty"`
	Digest                   *string         `json:"digest,omitempty"`
	CodeFingerprint          *string         `json:"code_fingerprint,omitempty"`
	EnclaveFingerprint       *string         `json:"enclave_fingerprint,omitempty"`
	TLSPublicKey             *string         `json:"tls_public_key,omitempty"`
	HPKEPublicKey            *string         `json:"hpke_public_key,omitempty"`
	TransportMode            *string         `json:"transport_mode,omitempty"`
	SDKVersion               *string         `json:"sdk_version,omitempty"`
	Status                   string          `json:"status"`
	FailureReason            *string         `json:"failure_reason,omitempty"`
	VerificationEvidenceJSON json.RawMessage `json:"verification_evidence_json,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
	VerifiedAt               *time.Time      `json:"verified_at,omitempty"`
}

func runtimeProofFromDomain(proof domain.TinfoilTransportProof) runtimeProof {
	return runtimeProof{
		ID: proof.ID, AccountID: proof.AccountID, APIKeyID: proof.APIKeyID, Provider: proof.Provider,
		PublicModelID: proof.PublicModelID, UpstreamModelID: proof.UpstreamModelID,
		ProviderResponseID: proof.ProviderResponseID, EnclaveHost: proof.EnclaveHost, ConfigRepo: proof.ConfigRepo,
		Digest: proof.Digest, CodeFingerprint: proof.CodeFingerprint, EnclaveFingerprint: proof.EnclaveFingerprint,
		TLSPublicKey: proof.TLSPublicKey, HPKEPublicKey: proof.HPKEPublicKey, TransportMode: proof.TransportMode,
		SDKVersion: proof.SDKVersion, Status: proof.Status, FailureReason: proof.FailureReason,
		VerificationEvidenceJSON: proof.VerificationEvidenceJSON, CreatedAt: proof.CreatedAt, VerifiedAt: proof.VerifiedAt,
	}
}

func (p runtimeProof) toDomain() domain.TinfoilTransportProof {
	return domain.TinfoilTransportProof{
		ID: p.ID, AccountID: p.AccountID, APIKeyID: p.APIKeyID, Provider: p.Provider,
		PublicModelID: p.PublicModelID, UpstreamModelID: p.UpstreamModelID,
		ProviderResponseID: p.ProviderResponseID, EnclaveHost: p.EnclaveHost, ConfigRepo: p.ConfigRepo,
		Digest: p.Digest, CodeFingerprint: p.CodeFingerprint, EnclaveFingerprint: p.EnclaveFingerprint,
		TLSPublicKey: p.TLSPublicKey, HPKEPublicKey: p.HPKEPublicKey, TransportMode: p.TransportMode,
		SDKVersion: p.SDKVersion, Status: p.Status, FailureReason: p.FailureReason,
		VerificationEvidenceJSON: p.VerificationEvidenceJSON, CreatedAt: p.CreatedAt, VerifiedAt: p.VerifiedAt,
	}
}
