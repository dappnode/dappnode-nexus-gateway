package mapper

import "testing"

func TestImageGenerationRequestToDomain(t *testing.T) {
	req, err := ImageGenerationRequestToDomain([]byte(`{"model":"flux","prompt":"a fox"}`))
	if err != nil || req.PublicModelID != "flux" || req.Images == nil || *req.Images != 1 {
		t.Fatalf("got %+v, %v; want model flux and 1 image by default", req, err)
	}
	req, err = ImageGenerationRequestToDomain([]byte(`{"model":"flux","prompt":"a fox","n":3,"size":"1024x1024","response_format":"b64_json"}`))
	if err != nil || *req.Images != 3 {
		t.Fatalf("got %+v, %v; want 3 images", req, err)
	}
	for name, body := range map[string]string{
		"no model":        `{"prompt":"a fox"}`,
		"no prompt":       `{"model":"flux"}`,
		"blank prompt":    `{"model":"flux","prompt":"  "}`,
		"n zero":          `{"model":"flux","prompt":"x","n":0}`,
		"n too large":     `{"model":"flux","prompt":"x","n":5}`,
		"n fractional":    `{"model":"flux","prompt":"x","n":1.5}`,
		"url output":      `{"model":"flux","prompt":"x","response_format":"url"}`,
		"streaming":       `{"model":"flux","prompt":"x","stream":true}`,
		"partial images":  `{"model":"flux","prompt":"x","partial_images":2}`,
		"invalid json":    `{`,
		"prompt not text": `{"model":"flux","prompt":7}`,
	} {
		if _, err := ImageGenerationRequestToDomain([]byte(body)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
