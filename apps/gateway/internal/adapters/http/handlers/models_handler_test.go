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
	rate := decimal.RequireFromString("1.5")
	cap := decimal.RequireFromString("0.50360083")
	data := toPublicModelData(domain.PublicModel{
		PublicModelID: "minimax/h3-image", Currency: "EUR", UpstreamModelName: "MiniMax-H3-Image",
		ProviderConfig:           domain.ProviderConfig{ProviderName: "minimax"},
		SupportsImageGenerations: true, ImageInputPricePerMillion: &rate, ImageMaxCostPerImage: &cap,
		InputPricePerMillion: decimal.NewFromInt(2), OutputPricePerMillion: decimal.NewFromInt(4),
	}, 1.1)
	if len(data.Endpoints) != 2 || data.Endpoints[0] != "images/generations" || data.Endpoints[1] != "images/edits" {
		t.Fatalf("endpoints = %v", data.Endpoints)
	}
	if data.ImageInputPricePer1MTokensCents == nil || *data.ImageInputPricePer1MTokensCents != 150 {
		t.Fatalf("reference token rate = %v", data.ImageInputPricePer1MTokensCents)
	}
	if data.ImageMaxCostPerImageCents == nil || *data.ImageMaxCostPerImageCents != 50.360083 {
		t.Fatalf("ceiling precision = %v", data.ImageMaxCostPerImageCents)
	}
	if data.ContextSize != nil || data.InputPricePer1MTokensCents == nil || *data.InputPricePer1MTokensCents != 200 {
		t.Fatal("image model needs token prices and no context size")
	}
}
