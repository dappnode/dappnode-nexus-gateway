package mapper

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"testing"
)

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

func TestImageEditRejectsRemoteAndInvalidReferences(t *testing.T) {
	for _, images := range []string{`[]`, `null`, `[{"image_url":"https://example.com/image.png"}]`, `[{"image_url":"data:image/png;base64,not-an-image"}]`} {
		if _, err := ImageGenerationRequestToDomain([]byte(`{"model":"minimax/h3-image","prompt":"edit","images":` + images + `}`)); err == nil {
			t.Errorf("accepted %s", images)
		}
	}
}

func TestImageEditValidatesReferenceDimensionsAndMIME(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		mime          string
		valid         bool
	}{
		{"valid", 128, 128, "png", true},
		{"too small", 127, 128, "png", false},
		{"too wide", 640, 128, "png", false},
		{"MIME mismatch", 128, 128, "jpeg", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, tc.width, tc.height))); err != nil {
				t.Fatal(err)
			}
			dataURL := "data:image/" + tc.mime + ";base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
			body, _ := json.Marshal(map[string]any{"model": "minimax/h3-image", "prompt": "edit", "images": []map[string]string{{"image_url": dataURL}}})
			_, err := ImageGenerationRequestToDomain(body)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}
