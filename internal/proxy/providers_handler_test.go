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

	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/proxy"
)

func TestProvidersHandlerReturnsCorrectFormat(t *testing.T) {
	t.Parallel()

	anthropicProvider := providers.NewAnthropicProviderWithModels(
		"anthropic-primary",
		"https://api.anthropic.com",
		[]string{"claude-sonnet-4-5-20250514", "claude-opus-4-5-20250514"},
	)

	handler := proxy.NewProvidersHandler([]providers.Provider{anthropicProvider})
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, proxy.JSONContentType, rec.Header().Get("Content-Type"))

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))

	assert.Equal(t, proxy.ListObject, response.Object)
	require.Len(t, response.Data, 1)

	provider := response.Data[0]
	assert.Equal(t, "anthropic-primary", provider.Name)
	assert.Equal(t, "anthropic", provider.Type)
	assert.Equal(t, "https://api.anthropic.com", provider.BaseURL)
	assert.True(t, provider.Active)
	require.Len(t, provider.Models, 2)
	assert.ElementsMatch(t, []string{"claude-sonnet-4-5-20250514", "claude-opus-4-5-20250514"}, provider.Models)
}

// serveProviders creates a GET /v1/providers request and records the response.
func serveProviders(t *testing.T, handler http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/v1/providers", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestProvidersHandlerMultipleProviders(t *testing.T) {
	t.Parallel()

	anthropicProvider := providers.NewAnthropicProviderWithModels(
		"anthropic-primary",
		"https://api.anthropic.com",
		[]string{"claude-sonnet-4-5-20250514"},
	)

	zaiProvider := providers.NewZAIProviderWithModels(
		"zai-primary",
		"https://open.bigmodel.cn/api/paas/v4",
		[]string{"glm-4", "glm-4-plus"},
	)

	handler := proxy.NewProvidersHandler([]providers.Provider{anthropicProvider, zaiProvider})
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Len(t, response.Data, 2)

	providerNames := make(map[string]bool)
	for _, p := range response.Data {
		providerNames[p.Name] = true
		assert.True(t, p.Active, "Provider %s should be active", p.Name)
	}

	assert.Contains(t, providerNames, "anthropic-primary")
	assert.Contains(t, providerNames, "zai-primary")

	for _, p := range response.Data {
		if p.Name == "zai-primary" {
			assert.Len(t, p.Models, 2, "zai-primary should have 2 models")
		}
	}
}

func TestProvidersHandlerEmptyProviders(t *testing.T) {
	t.Parallel()

	handler := proxy.NewProvidersHandler([]providers.Provider{})
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, proxy.ListObject, response.Object)
	assert.Empty(t, response.Data)
}

func TestProvidersHandlerProviderWithDefaultModels(t *testing.T) {
	t.Parallel()

	provider := providers.NewAnthropicProvider("anthropic", "https://api.anthropic.com")
	handler := proxy.NewProvidersHandler([]providers.Provider{provider})
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Len(t, response.Data, 1)

	expectedCount := len(providers.DefaultAnthropicModels)
	assert.Len(t, response.Data[0].Models, expectedCount)
}

func TestProvidersHandlerNilProviders(t *testing.T) {
	t.Parallel()

	handler := proxy.NewProvidersHandler(nil)
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, proxy.ListObject, response.Object)
	assert.Empty(t, response.Data)
}

func TestProvidersHandlerWithPoolsShowsKeyStatus(t *testing.T) {
	t.Parallel()

	provider := providers.NewAnthropicProviderWithModels(
		"anthropic-primary",
		"https://api.anthropic.com",
		[]string{"claude-sonnet-4-5-20250514"},
	)

	pool, err := keypool.NewKeyPool("anthropic-primary", keypool.PoolConfig{
		Strategy: "least_loaded",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-test-key-1", RPMLimit: 50},
			{APIKey: "sk-test-key-2", RPMLimit: 100},
		},
	})
	require.NoError(t, err)

	pools := map[string]*keypool.KeyPool{"anthropic-primary": pool}
	handler := proxy.NewProvidersHandlerWithPools([]providers.Provider{provider}, pools)
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Len(t, response.Data, 1)

	p := response.Data[0]
	assert.Equal(t, "anthropic-primary", p.Name)
	assert.Equal(t, 2, p.KeysTotal)
	assert.Equal(t, 2, p.KeysAvailable)
	require.Len(t, p.Keys, 2)

	for _, key := range p.Keys {
		assert.True(t, key.Available)
		assert.True(t, key.Healthy)
		assert.Equal(t, 0, key.CooldownSeconds)
		assert.Greater(t, key.RPMLimit, 0)
	}
}

func TestProvidersHandlerKeyStatusReflectsUnhealthy(t *testing.T) {
	t.Parallel()

	provider := providers.NewAnthropicProviderWithModels(
		"anthropic-primary",
		"https://api.anthropic.com",
		[]string{"claude-sonnet-4-5-20250514"},
	)

	pool, err := keypool.NewKeyPool("anthropic-primary", keypool.PoolConfig{
		Strategy: "least_loaded",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-healthy-key", RPMLimit: 50},
			{APIKey: "sk-unhealthy-key", RPMLimit: 50},
		},
	})
	require.NoError(t, err)

	// Mark one key as unhealthy
	poolKeys := pool.Keys()
	require.Len(t, poolKeys, 2)
	poolKeys[1].MarkUnhealthy(assert.AnError)

	pools := map[string]*keypool.KeyPool{"anthropic-primary": pool}
	handler := proxy.NewProvidersHandlerWithPools([]providers.Provider{provider}, pools)
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Len(t, response.Data, 1)

	p := response.Data[0]
	assert.Equal(t, 2, p.KeysTotal)
	assert.Equal(t, 1, p.KeysAvailable)

	var healthyFound, unhealthyFound bool
	for _, key := range p.Keys {
		if key.Healthy {
			healthyFound = true
			assert.True(t, key.Available)
		} else {
			unhealthyFound = true
			assert.False(t, key.Available)
		}
	}
	assert.True(t, healthyFound, "expected at least one healthy key")
	assert.True(t, unhealthyFound, "expected at least one unhealthy key")
}

func TestProvidersHandlerKeyStatusReflectsCooldown(t *testing.T) {
	t.Parallel()

	provider := providers.NewAnthropicProviderWithModels(
		"anthropic-primary",
		"https://api.anthropic.com",
		[]string{"claude-sonnet-4-5-20250514"},
	)

	pool, err := keypool.NewKeyPool("anthropic-primary", keypool.PoolConfig{
		Strategy: "least_loaded",
		Keys: []keypool.KeyConfig{
			{APIKey: "sk-available-key", RPMLimit: 50},
			{APIKey: "sk-cooldown-key", RPMLimit: 50},
		},
	})
	require.NoError(t, err)

	// Put one key in cooldown for 30 seconds
	poolKeys := pool.Keys()
	require.Len(t, poolKeys, 2)
	poolKeys[1].SetCooldown(time.Now().Add(30 * time.Second))

	pools := map[string]*keypool.KeyPool{"anthropic-primary": pool}
	handler := proxy.NewProvidersHandlerWithPools([]providers.Provider{provider}, pools)
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Len(t, response.Data, 1)

	p := response.Data[0]
	assert.Equal(t, 2, p.KeysTotal)
	assert.Equal(t, 1, p.KeysAvailable)

	for _, key := range p.Keys {
		if !key.Available {
			assert.True(t, key.Healthy, "cooled-down key should still be healthy")
			assert.Greater(t, key.CooldownSeconds, 0, "cooldown key should have remaining seconds")
		}
	}
}

func TestProvidersHandlerWithProviderFuncAndPools(t *testing.T) {
	t.Parallel()

	provider := providers.NewAnthropicProviderWithModels(
		"anthropic-primary",
		"https://api.anthropic.com",
		[]string{"claude-sonnet-4-5-20250514"},
	)

	pool, err := keypool.NewKeyPool("anthropic-primary", keypool.PoolConfig{
		Strategy: "round_robin",
		Keys:     []keypool.KeyConfig{{APIKey: "sk-live-key", RPMLimit: 50}},
	})
	require.NoError(t, err)

	providersGetter := func() []providers.Provider {
		return []providers.Provider{provider}
	}
	poolsGetter := func() map[string]*keypool.KeyPool {
		return map[string]*keypool.KeyPool{"anthropic-primary": pool}
	}

	handler := proxy.NewProvidersHandlerWithProviderFuncAndPools(providersGetter, poolsGetter)
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Len(t, response.Data, 1)

	p := response.Data[0]
	assert.Equal(t, 1, p.KeysTotal)
	assert.Equal(t, 1, p.KeysAvailable)
	require.Len(t, p.Keys, 1)
	assert.True(t, p.Keys[0].Available)
}

func TestProvidersHandlerNoPoolOmitsKeys(t *testing.T) {
	t.Parallel()

	provider := providers.NewAnthropicProviderWithModels(
		"anthropic-primary",
		"https://api.anthropic.com",
		[]string{"claude-sonnet-4-5-20250514"},
	)

	// No pool provided — backward compatible path
	handler := proxy.NewProvidersHandler([]providers.Provider{provider})
	rec := serveProviders(t, handler)

	require.Equal(t, http.StatusOK, rec.Code)

	var response proxy.ProvidersResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Len(t, response.Data, 1)

	p := response.Data[0]
	assert.Equal(t, 0, p.KeysTotal)
	assert.Equal(t, 0, p.KeysAvailable)
	assert.Empty(t, p.Keys)
}
