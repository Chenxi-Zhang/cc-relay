package providers_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/omarluq/cc-relay/internal/providers"
)

const testOpenAIName = "test-openai"

func TestOpenAIProviderInterface(t *testing.T) {
	t.Parallel()

	// Verify OpenAIProvider implements Provider interface
	var _ providers.Provider = (*providers.OpenAIProvider)(nil)
}

func TestOpenAINameAndBaseURL(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		"my-openai", "https://api.openai.com/v1", nil, nil,
	)

	if provider.Name() != "my-openai" {
		t.Errorf("Expected name=my-openai, got %s", provider.Name())
	}

	if provider.BaseURL() != "https://api.openai.com/v1" {
		t.Errorf("Expected baseURL=https://api.openai.com/v1, got %s", provider.BaseURL())
	}
}

func TestOpenAIOwner(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	if provider.Owner() != "openai" {
		t.Errorf("Expected owner=openai, got %s", provider.Owner())
	}
}

func TestOpenAIAuthenticate(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	testURL := "https://api.openai.com/v1/chat/completions"
	req, err := http.NewRequestWithContext(
		context.Background(), "POST", testURL, http.NoBody,
	)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	testAuthKey := "test-auth-key-for-testing-only"

	err = provider.Authenticate(req, testAuthKey)
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	// OpenAI uses Bearer token authentication
	gotAuth := req.Header.Get("Authorization")
	wantAuth := "Bearer " + testAuthKey

	if gotAuth != wantAuth {
		t.Errorf("Expected Authorization=%s, got %s", wantAuth, gotAuth)
	}

	// Verify x-api-key is NOT set
	if req.Header.Get("x-api-key") != "" {
		t.Error("Expected x-api-key header to NOT be set for OpenAI")
	}
}

func TestOpenAIForwardHeadersNoAnthropicHeaders(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	// Create original headers with anthropic-* headers that should NOT be forwarded
	originalHeaders := http.Header{
		"anthropic-version":                         []string{"2023-06-01"},
		"anthropic-dangerous-direct-browser-access": []string{"true"},
		"anthropic-beta":                            []string{"feature-1"},
		"Authorization":                             []string{"Bearer token"},
		"User-Agent":                                []string{"test-agent"},
		"X-Custom-Header":                           []string{"custom-value"},
	}

	forwardedHeaders := provider.ForwardHeaders(originalHeaders)

	// Verify anthropic-* headers are NOT forwarded
	if forwardedHeaders.Get("anthropic-version") != "" {
		t.Error("Expected anthropic-version header to NOT be forwarded for OpenAI")
	}

	if forwardedHeaders.Get("anthropic-dangerous-direct-browser-access") != "" {
		t.Error("Expected anthropic-dangerous-direct-browser-access header to NOT be forwarded for OpenAI")
	}

	if forwardedHeaders.Get("anthropic-beta") != "" {
		t.Error("Expected anthropic-beta header to NOT be forwarded for OpenAI")
	}

	// Verify Content-Type is set
	if forwardedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type=application/json, got %s", forwardedHeaders.Get("Content-Type"))
	}

	// Verify non-anthropic headers are NOT forwarded
	if forwardedHeaders.Get("Authorization") != "" {
		t.Error("Expected Authorization header to NOT be forwarded")
	}

	if forwardedHeaders.Get("User-Agent") != "" {
		t.Error("Expected User-Agent header to NOT be forwarded")
	}

	if forwardedHeaders.Get("X-Custom-Header") != "" {
		t.Error("Expected X-Custom-Header to NOT be forwarded")
	}
}

func TestOpenAIForwardHeadersEmpty(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	forwardedHeaders := provider.ForwardHeaders(http.Header{})

	// Only Content-Type should be set
	if forwardedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type=application/json, got %s", forwardedHeaders.Get("Content-Type"))
	}

	if len(forwardedHeaders) != 1 {
		t.Errorf("Expected exactly 1 header (Content-Type), got %d headers", len(forwardedHeaders))
	}
}

func TestOpenAISupportsStreaming(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	if !provider.SupportsStreaming() {
		t.Error("Expected OpenAIProvider to support streaming")
	}
}

func TestOpenAISupportsTransparentAuth(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	if provider.SupportsTransparentAuth() {
		t.Error("Expected OpenAIProvider to NOT support transparent auth")
	}
}

func TestOpenAIStreamingContentType(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	if provider.StreamingContentType() != "text/event-stream" {
		t.Errorf("Expected StreamingContentType=text/event-stream, got %s", provider.StreamingContentType())
	}
}

func TestOpenAIRequiresBodyTransform(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	if provider.RequiresBodyTransform() {
		t.Error("Expected OpenAIProvider to NOT require body transform")
	}
}

func TestOpenAITransformRequest(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	body := []byte(`{"model":"gpt-4","messages":[]}`)
	newBody, targetURL, err := provider.TransformRequest(body, "/chat/completions")
	if err != nil {
		t.Fatalf("TransformRequest failed: %v", err)
	}

	// Body should be unchanged
	if string(newBody) != string(body) {
		t.Errorf("Expected body unchanged, got %s", string(newBody))
	}

	// URL should be baseURL + endpoint
	wantURL := "https://api.openai.com/v1/chat/completions"
	if targetURL != wantURL {
		t.Errorf("Expected targetURL=%s, got %s", wantURL, targetURL)
	}
}

func TestOpenAITransformResponse(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	// TransformResponse should return nil (passthrough)
	if provider.TransformResponse(nil, nil) != nil {
		t.Error("Expected TransformResponse to return nil")
	}
}

func TestOpenAIListModels(t *testing.T) {
	t.Parallel()

	models := []string{"gpt-4", "gpt-4o"}
	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", models, nil,
	)

	result := provider.ListModels()

	if len(result) != 2 {
		t.Fatalf("Expected 2 models, got %d", len(result))
	}

	if result[0].ID != "gpt-4" {
		t.Errorf("Expected model ID=gpt-4, got %s", result[0].ID)
	}

	if result[1].ID != "gpt-4o" {
		t.Errorf("Expected model ID=gpt-4o, got %s", result[1].ID)
	}
}

func TestOpenAIListModelsEmpty(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	result := provider.ListModels()

	if len(result) != 0 {
		t.Errorf("Expected 0 models for nil input, got %d", len(result))
	}
}

func TestOpenAIModelMapping(t *testing.T) {
	t.Parallel()

	mapping := map[string]string{
		"claude-sonnet-4-5-20250514": "gpt-4o",
	}
	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, mapping,
	)

	// Mapped model should resolve
	if provider.MapModel("claude-sonnet-4-5-20250514") != "gpt-4o" {
		t.Error("Expected model mapping to resolve")
	}

	// Unmapped model should pass through
	if provider.MapModel("unknown-model") != "unknown-model" {
		t.Error("Expected unmapped model to pass through")
	}
}

func TestOpenAIGetModelMapping(t *testing.T) {
	t.Parallel()

	mapping := map[string]string{
		"claude-sonnet-4-5-20250514": "gpt-4o",
	}
	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, mapping,
	)

	got := provider.GetModelMapping()
	if got == nil {
		t.Fatal("Expected non-nil model mapping")
	}

	if got["claude-sonnet-4-5-20250514"] != "gpt-4o" {
		t.Errorf("Expected mapping claude-sonnet-4-5-20250514 -> gpt-4o, got %s", got["claude-sonnet-4-5-20250514"])
	}
}

func TestOpenAIGetModelMappingNil(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		testOpenAIName, "https://api.openai.com/v1", nil, nil,
	)

	if provider.GetModelMapping() != nil {
		t.Error("Expected nil model mapping")
	}
}
