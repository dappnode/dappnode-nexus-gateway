package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
	"github.com/shopspring/decimal"
)

// The operator's record is published unchanged, including fields this Gateway
// does not know yet. There is no separate features list any more.
func TestModelsHandler_PublishesCapabilitiesUnchanged(t *testing.T) {
	stored := `{"modalities":{"input":["text","image"],"output":["text"]},"tool_call":true,"reasoning":true,` +
		`"reasoning_options":[{"type":"effort","values":["low","high","max"],"default":"high"}],"audio_output":false}`
	data := toPublicModelData(domain.PublicModel{PublicModelID: "vision", SupportsChatCompletions: true, Capabilities: json.RawMessage(stored)}, 1)
	if string(data.Capabilities) != stored {
		t.Fatalf("capabilities = %s", data.Capabilities)
	}
	if body, _ := json.Marshal(data); strings.Contains(string(body), `"features"`) {
		t.Fatalf("published the removed features list: %s", body)
	}
	empty := toPublicModelData(domain.PublicModel{PublicModelID: "unknown", SupportsChatCompletions: true, Capabilities: json.RawMessage(`{}`)}, 1)
	body, _ := json.Marshal(empty)
	if strings.Contains(string(body), `"capabilities"`) {
		t.Fatalf("published capabilities nobody stated: %s", body)
	}
}

func TestToPublicModelData_ImageModel(t *testing.T) {
	price := decimal.RequireFromString("0.015")
	data := toPublicModelData(domain.PublicModel{
		PublicModelID: "flux", Currency: "EUR",
		SupportsImageGenerations: true, PricePerImage: &price,
	}, 1.1)
	if len(data.Endpoints) != 1 || data.Endpoints[0] != "images/generations" {
		t.Fatalf("endpoints = %v, want images/generations", data.Endpoints)
	}
	if data.PricePerImageCents == nil || *data.PricePerImageCents != 1.5 {
		t.Fatalf("price per image = %v cents, want 1.5", data.PricePerImageCents)
	}
	if data.PricePerImageUSD == nil || *data.PricePerImageUSD != 0.0165 {
		t.Fatalf("price per image = %v USD, want 0.0165", data.PricePerImageUSD)
	}
	if data.ContextSize != nil || data.InputPricePer1MTokensCents != nil {
		t.Fatal("an image model publishes no context size or token prices")
	}
}

func TestToPublicModelData_ImagePricePreservesMicrocents(t *testing.T) {
	price := decimal.RequireFromString("0.00360083")
	data := toPublicModelData(domain.PublicModel{Currency: "EUR", SupportsImageGenerations: true, PricePerImage: &price}, 1)
	if data.PricePerImageCents == nil || *data.PricePerImageCents != 0.360083 {
		t.Fatalf("price = %v, want 0.360083 cents", data.PricePerImageCents)
	}
}
