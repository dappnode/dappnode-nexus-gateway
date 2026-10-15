package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
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
