package providers_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/omarluq/cc-relay/internal/providers"
)

const testGenericName = "test-generic"

func TestGenericProviderWithXAPIKey(t *testing.T) {
	t.Parallel()

	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com/v1", nil, nil, "",
	)

	testURL := "https://api.example.com/v1/v1/messages"
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

	gotKey := req.Header.Get("x-api-key")
	if gotKey != testAuthKey {
		t.Errorf("Expected x-api-key=%s, got %s", testAuthKey, gotKey)
	}

	if req.Header.Get("Authorization") != "" {
		t.Error("Expected Authorization header to not be set for x-api-key auth")
	}
}

func TestGenericProviderWithBearerAuth(t *testing.T) {
	t.Parallel()

	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com/v1", nil, nil, "bearer",
	)

	testURL := "https://api.example.com/v1/v1/messages"
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

	gotAuth := req.Header.Get("Authorization")
	wantAuth := "Bearer " + testAuthKey
	if gotAuth != wantAuth {
		t.Errorf("Expected Authorization=%s, got %s", wantAuth, gotAuth)
	}

	if req.Header.Get("x-api-key") != "" {
		t.Error("Expected x-api-key header to not be set for bearer auth")
	}
}

func TestGenericProviderNameAndURL(t *testing.T) {
	t.Parallel()

	provider := providers.NewGenericProviderWithMapping(
		"my-service", "https://api.example.com/v1", nil, nil, "",
	)

	if provider.Name() != "my-service" {
		t.Errorf("Expected name=my-service, got %s", provider.Name())
	}

	if provider.BaseURL() != "https://api.example.com/v1" {
		t.Errorf("Expected baseURL=https://api.example.com/v1, got %s", provider.BaseURL())
	}
}

func TestGenericProviderOwner(t *testing.T) {
	t.Parallel()

	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com", nil, nil, "",
	)

	if provider.Owner() != "generic" {
		t.Errorf("Expected owner=generic, got %s", provider.Owner())
	}
}

func TestGenericProviderInterface(t *testing.T) {
	t.Parallel()

	var _ providers.Provider = (*providers.GenericProvider)(nil)
}

func TestGenericProviderSupportsStreaming(t *testing.T) {
	t.Parallel()

	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com", nil, nil, "",
	)

	if !provider.SupportsStreaming() {
		t.Error("Expected GenericProvider to support streaming")
	}
}

func TestGenericProviderNoTransparentAuth(t *testing.T) {
	t.Parallel()

	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com", nil, nil, "",
	)

	if provider.SupportsTransparentAuth() {
		t.Error("Expected GenericProvider to NOT support transparent auth")
	}
}

func TestGenericProviderListModels(t *testing.T) {
	t.Parallel()

	models := []string{"model-a", "model-b"}
	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com", models, nil, "",
	)

	result := provider.ListModels()

	if len(result) != 2 {
		t.Fatalf("Expected 2 models, got %d", len(result))
	}

	if result[0].ID != "model-a" {
		t.Errorf("Expected model ID=model-a, got %s", result[0].ID)
	}

	if result[1].ID != "model-b" {
		t.Errorf("Expected model ID=model-b, got %s", result[1].ID)
	}
}

func TestGenericProviderEmptyModels(t *testing.T) {
	t.Parallel()

	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com", nil, nil, "",
	)

	result := provider.ListModels()

	if len(result) != 0 {
		t.Errorf("Expected 0 models for nil input, got %d", len(result))
	}
}

func TestGenericProviderModelMapping(t *testing.T) {
	t.Parallel()

	mapping := map[string]string{
		"claude-sonnet-4-5-20250514": "custom-model-1",
	}
	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com", nil, mapping, "",
	)

	if provider.MapModel("claude-sonnet-4-5-20250514") != "custom-model-1" {
		t.Error("Expected model mapping to resolve")
	}

	if provider.MapModel("unknown-model") != "unknown-model" {
		t.Error("Expected unmapped model to pass through")
	}
}

func TestGenericProviderForwardHeaders(t *testing.T) {
	t.Parallel()

	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com", nil, nil, "",
	)

	assertForwardHeaders(t, provider)
}

func TestGenericProviderForwardHeadersEdgeCases(t *testing.T) {
	t.Parallel()

	provider := providers.NewGenericProviderWithMapping(
		testGenericName, "https://api.example.com", nil, nil, "",
	)

	assertForwardHeadersEdgeCases(t, provider)
}
