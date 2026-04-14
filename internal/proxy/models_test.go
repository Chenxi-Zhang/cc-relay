// Package proxy_test implements tests for the HTTP proxy server.
package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/proxy"
)

// serveModels creates a GET /v1/models request and records the response.
func serveModels(t *testing.T, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/models", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestModelsHandlerReturnsCorrectFormat(t *testing.T) {
	t.Parallel()

	anthropicProvider := providers.NewAnthropicProviderWithModels(
		"anthropic-primary",
		"https://api.anthropic.com",
		[]string{"claude-sonnet-4-5-20250514", "claude-opus-4-5-20250514"},
	)

	handler := proxy.NewModelsHandler([]providers.Provider{anthropicProvider})
	rec := serveModels(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, proxy.JSONContentType, rec.Header().Get("Content-Type"))

	var response proxy.ModelsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))

	require.Len(t, response.Data, 2)
	assert.Equal(t, "claude-sonnet-4-5-20250514", response.Data[0].ID)
	assert.Equal(t, "model", response.Data[0].Type)
	assert.Equal(t, "claude-sonnet-4-5-20250514", response.Data[0].DisplayName)
	assert.NotEmpty(t, response.Data[0].CreatedAt)
	_, err := time.Parse(time.RFC3339, response.Data[0].CreatedAt)
	assert.NoError(t, err, "created_at should be valid RFC 3339")

	assert.False(t, response.HasMore)
	require.NotNil(t, response.FirstID)
	assert.Equal(t, "claude-sonnet-4-5-20250514", *response.FirstID)
	require.NotNil(t, response.LastID)
	assert.Equal(t, "claude-opus-4-5-20250514", *response.LastID)
}

func TestModelsHandlerMultipleProviders(t *testing.T) {
	t.Parallel()

	anthropicProvider := providers.NewAnthropicProviderWithModels(
		"anthropic-primary",
		"https://api.anthropic.com",
		[]string{"claude-sonnet-4-5-20250514"},
	)

	zaiProvider := providers.NewZAIProviderWithModels(
		"zai-primary",
		"",
		[]string{"glm-4", "glm-4-plus"},
	)

	handler := proxy.NewModelsHandler([]providers.Provider{anthropicProvider, zaiProvider})

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/models", http.NoBody)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", rec.Code)
	}

	var response proxy.ModelsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Should have 3 models total (1 from anthropic + 2 from zai)
	if len(response.Data) != 3 {
		t.Fatalf("Expected 3 models, got %d", len(response.Data))
	}

	// Verify models from both providers are present
	modelIDs := make(map[string]bool)
	for _, m := range response.Data {
		modelIDs[m.ID] = true
	}

	expectedModels := []string{"claude-sonnet-4-5-20250514", "glm-4", "glm-4-plus"}
	for _, expected := range expectedModels {
		if !modelIDs[expected] {
			t.Errorf("Expected model %s to be present", expected)
		}
	}

	assert.False(t, response.HasMore)
	require.NotNil(t, response.FirstID)
	require.NotNil(t, response.LastID)
}

func TestModelsHandlerEmptyProviders(t *testing.T) {
	t.Parallel()

	handler := proxy.NewModelsHandler([]providers.Provider{})
	rec := serveModels(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ModelsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Empty(t, response.Data)
	assert.False(t, response.HasMore)
	assert.Nil(t, response.FirstID)
	assert.Nil(t, response.LastID)
}

func TestModelsHandlerProviderWithDefaultModels(t *testing.T) {
	t.Parallel()

	provider := providers.NewAnthropicProvider("anthropic", "https://api.anthropic.com")
	handler := proxy.NewModelsHandler([]providers.Provider{provider})
	rec := serveModels(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ModelsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Len(t, response.Data, len(providers.DefaultAnthropicModels))
	assert.False(t, response.HasMore)
	require.NotNil(t, response.FirstID)
	require.NotNil(t, response.LastID)
}

func TestModelsHandlerNilProviders(t *testing.T) {
	t.Parallel()

	handler := proxy.NewModelsHandler(nil)
	rec := serveModels(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ModelsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Empty(t, response.Data)
	assert.False(t, response.HasMore)
	assert.Nil(t, response.FirstID)
	assert.Nil(t, response.LastID)
}
