package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

func miniMaxModel(baseURL string) domain.PublicModel {
	return domain.PublicModel{
		PublicModelID: "minimax/image-01", UpstreamModelName: "image-01",
		ProviderConfig: domain.ProviderConfig{ProviderName: "minimax", BaseURL: baseURL, APIKeySecretRef: "MINIMAX_TEST_API_KEY"},
	}
}

func TestMiniMaxImagesTranslatesBothWays(t *testing.T) {
	t.Setenv("MINIMAX_TEST_API_KEY", "k")
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/image_generation" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("request to %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"t1","data":{"image_base64":["aW1n","aW1nMg=="]},"metadata":{"success_count":"2","failed_count":"0"},"base_resp":{"status_code":0,"status_msg":"success"}}`))
	}))
	defer server.Close()

	out, err := NewAdapter(time.Minute).GenerateImages(context.Background(),
		[]byte(`{"model":"minimax/image-01","prompt":"a fox","n":2,"size":"1024x768","quality":"hd","user":"u"}`), miniMaxModel(server.URL))
	if err != nil {
		t.Fatalf("GenerateImages: %v", err)
	}
	if got["model"] != "image-01" || got["response_format"] != "base64" || got["width"] != float64(1024) || got["height"] != float64(768) {
		t.Fatalf("MiniMax got %v", got)
	}
	if _, ok := got["quality"]; ok {
		t.Fatal("fields MiniMax doesn't document must not be sent")
	}
	var resp struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || len(resp.Data) != 2 || resp.Data[1].B64JSON != "aW1nMg==" {
		t.Fatalf("OpenAI response = %s, %v", out, err)
	}
}

func TestMiniMaxImageErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		code string
	}{
		"sensitive prompt":       {`{"base_resp":{"status_code":1026,"status_msg":"input new_sensitive"}}`, domain.ErrCodeContentPolicy},
		"every image blocked":    {`{"data":{"image_base64":[]},"metadata":{"success_count":"0","failed_count":"1"},"base_resp":{"status_code":0}}`, domain.ErrCodeContentPolicy},
		"rate limit":             {`{"base_resp":{"status_code":1002,"status_msg":"rate limit"}}`, domain.ErrCodeProviderRateLimited},
		"nexus balance":          {`{"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`, domain.ErrCodeProviderConfiguration},
		"bad parameters":         {`{"base_resp":{"status_code":2013,"status_msg":"invalid params"}}`, domain.ErrCodeProviderRejectedRequest},
		"unknown failure":        {`{"base_resp":{"status_code":1000,"status_msg":"unknown"}}`, domain.ErrCodeProviderError},
		"nothing and no reasons": {`{"data":{"image_base64":[]},"base_resp":{"status_code":0}}`, domain.ErrCodeProviderError},
	} {
		_, err := miniMaxToOpenAIImages([]byte(tc.body))
		var gwErr *domain.GatewayError
		if !errors.As(err, &gwErr) || gwErr.Code != tc.code {
			t.Errorf("%s: err = %v, want %s", name, err, tc.code)
		}
	}
}

func TestMiniMaxImageSize(t *testing.T) {
	for size, ok := range map[string]bool{"1024x1024": true, "512x2048": true, "1024x1000": true, "500x500": false, "1025x1024": false, "big": false} {
		_, err := miniMaxImageBody([]byte(`{"prompt":"x","size":"`+size+`"}`), miniMaxModel(""))
		if (err == nil) != ok {
			t.Errorf("size %s: err = %v", size, err)
		}
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
