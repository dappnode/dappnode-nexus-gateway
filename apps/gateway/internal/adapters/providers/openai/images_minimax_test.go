package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func miniMaxModel(baseURL string) domain.PublicModel {
	return domain.PublicModel{
		PublicModelID: "minimax/h3-image", UpstreamModelName: "MiniMax-H3-Image", SupportsImageGenerations: true,
		ProviderConfig: domain.ProviderConfig{ProviderName: "minimax", BaseURL: baseURL, APIKeySecretRef: "MINIMAX_TEST_API_KEY"},
	}
}

func TestMiniMaxH3EditsKeepsUsageAndOnlySuccessfulImages(t *testing.T) {
	t.Setenv("MINIMAX_TEST_API_KEY", "k")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/images/edits" || body["area"] != "1K" || body["response_format"] != "b64_json" || body["model"] != "MiniMax-H3-Image" {
			t.Errorf("unexpected H3 request: %s %v", r.URL.Path, body)
		}
		if len(body["images"].([]any)) != 1 {
			t.Error("lost reference image")
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"aW1n"},{"error":{"code":"1027"}}],"usage":{"generated_images":1,"output_tokens":8000,"input_tokens_details":{"text_tokens":200,"image_tokens":1000}}}`))
	}))
	defer server.Close()
	model := miniMaxModel(server.URL)
	model.UpstreamModelName = "MiniMax-H3-Image"
	model.SupportsImageGenerations = true
	out, err := NewAdapter(time.Second).GenerateImages(context.Background(), []byte(`{"model":"public","prompt":"change background","images":[{"image_url":"data:image/png;base64,abc"}],"n":2}`), model)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Data  []any
		Usage json.RawMessage
	}
	if json.Unmarshal(out, &got) != nil || len(got.Data) != 1 || len(got.Usage) == 0 {
		t.Fatalf("bad response: %s", out)
	}
}

func TestMiniMaxH3GenerationWithoutReferences(t *testing.T) {
	t.Setenv("MINIMAX_TEST_API_KEY", "k")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/images/generations" || body["area"] != "1K" || body["model"] != "MiniMax-H3-Image" {
			t.Errorf("unexpected generation: %s %v", r.URL.Path, body)
		}
		if _, ok := body["images"]; ok {
			t.Error("text-to-image request gained references")
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"aW1n"}],"usage":{"generated_images":1}}`))
	}))
	defer server.Close()
	if _, err := NewAdapter(time.Second).GenerateImages(context.Background(), []byte(`{"model":"public","prompt":"a bird","n":1}`), miniMaxModel(server.URL)); err != nil {
		t.Fatal(err)
	}
}

func TestImageAdapterRejectsUnsupportedModel(t *testing.T) {
	model := miniMaxModel("http://unused")
	model.UpstreamModelName = "unsupported-image-model"
	if _, err := NewAdapter(time.Second).GenerateImages(context.Background(), []byte(`{"prompt":"a bird"}`), model); err == nil {
		t.Fatal("unsupported model accepted")
	}
}
