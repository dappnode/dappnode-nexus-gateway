package services

import (
	"encoding/json"
	"math"
	"net/http"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func observeSystemOne(body []byte, public string, unmask *unmasker) ([]byte, outcome, error) {
	var response struct {
		Model   *string                    `json:"model"`
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   *struct {
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || response.Model == nil || len(response.Answers) == 0 ||
		response.Usage == nil || response.Usage.Input == nil || response.Usage.Output == nil {
		return nil, outcome{}, domain.ErrProviderError(http.StatusBadGateway, "invalid systemone response or missing usage")
	}
	input, output := *response.Usage.Input, *response.Usage.Output
	if input < 0 || output < 0 || input > math.MaxInt64-output {
		return nil, outcome{}, domain.ErrProviderError(http.StatusBadGateway, "invalid systemone usage")
	}
	if unmask != nil {
		body = unmask.body(body)
		unmask.report(false)
	}
	return rewriteModel(body, &chunk{Model: response.Model}, public), outcome{
		usage: &domain.Usage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output}, end: "complete",
	}, nil
}
