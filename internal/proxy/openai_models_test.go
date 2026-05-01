package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/proxy"
)

func serveOpenAIModels(t *testing.T, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/openai/v1/models", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestOpenAIModelsHandlerEmpty(t *testing.T) {
	t.Parallel()

	handler := proxy.NewOpenAIModelsHandler([]providers.Provider{})
	rec := serveOpenAIModels(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, proxy.JSONContentType, rec.Header().Get("Content-Type"))

	var response proxy.OpenAIModelsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "list", response.Object)
	assert.Empty(t, response.Data)
}

func TestOpenAIModelsHandlerNilProviders(t *testing.T) {
	t.Parallel()

	handler := proxy.NewOpenAIModelsHandler(nil)
	rec := serveOpenAIModels(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.OpenAIModelsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "list", response.Object)
	assert.Empty(t, response.Data)
}

func TestOpenAIModelsHandlerSingleProvider(t *testing.T) {
	t.Parallel()

	provider := providers.NewOpenAIProviderWithMapping(
		"deepseek",
		"https://api.deepseek.com",
		[]string{"deepseek-chat", "deepseek-reasoner"},
		nil,
	)

	handler := proxy.NewOpenAIModelsHandler([]providers.Provider{provider})
	rec := serveOpenAIModels(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.OpenAIModelsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "list", response.Object)
	require.Len(t, response.Data, 2)

	assert.Equal(t, "deepseek-chat", response.Data[0].ID)
	assert.Equal(t, "model", response.Data[0].Object)
	assert.Greater(t, response.Data[0].Created, int64(0))
	assert.Equal(t, "openai", response.Data[0].OwnedBy)

	assert.Equal(t, "deepseek-reasoner", response.Data[1].ID)
	assert.Equal(t, "model", response.Data[1].Object)
	assert.Greater(t, response.Data[1].Created, int64(0))
	assert.Equal(t, "openai", response.Data[1].OwnedBy)
}

func TestOpenAIModelsHandlerMultipleProviders(t *testing.T) {
	t.Parallel()

	provider1 := providers.NewOpenAIProviderWithMapping(
		"deepseek",
		"https://api.deepseek.com",
		[]string{"deepseek-chat"},
		nil,
	)

	provider2 := providers.NewOpenAIProviderWithMapping(
		"zhipu",
		"https://api.zhipuai.com",
		[]string{"glm-4", "glm-4-plus"},
		nil,
	)

	handler := proxy.NewOpenAIModelsHandler([]providers.Provider{provider1, provider2})
	rec := serveOpenAIModels(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.OpenAIModelsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "list", response.Object)
	require.Len(t, response.Data, 3)

	modelIDs := make(map[string]bool)
	for _, m := range response.Data {
		modelIDs[m.ID] = true
		assert.Equal(t, "model", m.Object)
		assert.Greater(t, m.Created, int64(0))
		assert.Equal(t, "openai", m.OwnedBy)
	}

	assert.True(t, modelIDs["deepseek-chat"])
	assert.True(t, modelIDs["glm-4"])
	assert.True(t, modelIDs["glm-4-plus"])
}

func TestOpenAIModelsHandlerWithProviderFunc(t *testing.T) {
	t.Parallel()

	provider1 := providers.NewOpenAIProviderWithMapping(
		"deepseek",
		"https://api.deepseek.com",
		[]string{"deepseek-chat"},
		nil,
	)

	callCount := 0
	handler := proxy.NewOpenAIModelsHandlerWithProviderFunc(func() []providers.Provider {
		callCount++
		return []providers.Provider{provider1}
	})

	rec := serveOpenAIModels(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, callCount)

	var response proxy.OpenAIModelsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "list", response.Object)
	require.Len(t, response.Data, 1)
	assert.Equal(t, "deepseek-chat", response.Data[0].ID)
	assert.Equal(t, "openai", response.Data[0].OwnedBy)
}
