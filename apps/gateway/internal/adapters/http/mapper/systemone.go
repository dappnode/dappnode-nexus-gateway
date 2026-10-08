package mapper

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// SystemOneRequestToDomain reads routing fields while preserving the native body.
func SystemOneRequestToDomain(raw []byte) (domain.GenerateRequest, error) {
	var body struct {
		Model     string          `json:"model"`
		State     json.RawMessage `json:"state"`
		Questions map[string]struct {
			Type         string          `json:"type"`
			Instructions json.RawMessage `json:"instructions"`
			Criteria     json.RawMessage `json:"criteria"`
		} `json:"questions"`
		Stream bool `json:"stream"`
	}
	invalid := func(message string) (domain.GenerateRequest, error) {
		err := domain.ErrInvalidField(message)
		err.HTTPStatus = 422
		return domain.GenerateRequest{}, err
	}
	if json.Unmarshal(raw, &body) != nil {
		return invalid("invalid systemone request")
	}
	if strings.TrimSpace(body.Model) == "" {
		return invalid("model is required")
	}
	if body.Stream {
		return invalid("systemone does not support streaming")
	}
	if !structuredValue(body.State) {
		return invalid("state must be a string, object, or array")
	}
	if len(body.Questions) == 0 {
		return invalid("questions must be a non-empty object")
	}
	for _, q := range body.Questions {
		if !structuredValue(q.Instructions) {
			return invalid("question instructions must be a string, object, or array")
		}
		switch q.Type {
		case "noul":
			if len(q.Criteria) == 0 {
				continue
			}
			var criteria map[string]json.RawMessage
			if json.Unmarshal(q.Criteria, &criteria) != nil || criteria == nil {
				return invalid("noul criteria must be an object")
			}
			for key, value := range criteria {
				if (key != "true" && key != "false") || !structuredValue(value) {
					return invalid("noul criteria must describe true or false")
				}
			}
		case "choice":
			var criteria map[string]json.RawMessage
			if json.Unmarshal(q.Criteria, &criteria) != nil || len(criteria) < 1 || len(criteria) > 255 {
				return invalid("choice criteria must contain 1 to 255 options")
			}
			for _, value := range criteria {
				if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !structuredValue(value) {
					return invalid("invalid choice criterion")
				}
			}
		case "score":
			var criteria []json.RawMessage
			if json.Unmarshal(q.Criteria, &criteria) != nil || len(criteria) < 2 || len(criteria) > 10 {
				return invalid("score criteria must contain 2 to 10 levels")
			}
			for _, value := range criteria {
				if !structuredValue(value) {
					return invalid("invalid score criterion")
				}
			}
		default:
			return invalid("question type must be noul, choice, or score")
		}
	}
	return domain.GenerateRequest{PublicModelID: body.Model, RequestedModelID: body.Model}, nil
}

func structuredValue(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && (raw[0] == '"' || raw[0] == '{' || raw[0] == '[')
}
