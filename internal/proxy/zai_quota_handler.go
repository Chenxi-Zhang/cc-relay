package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
)

// ZAIQuotaHandler handles on-demand GLM Coding Plan quota lookups.
type ZAIQuotaHandler struct {
	getProviders     ProvidersGetter
	providers        []providers.Provider
	getProviderPools func() map[string]*keypool.KeyPool
	providerPools    map[string]*keypool.KeyPool
	getProviderKeys  KeysFunc
	providerKeys     map[string]string
	isZhipuProvider  func(string) bool
	client           *http.Client
}

// NewZAIQuotaHandlerWithProviderFunc creates a live Z.AI quota handler.
func NewZAIQuotaHandlerWithProviderFunc(
	getProviders ProvidersGetter,
	getPools func() map[string]*keypool.KeyPool,
	getKeys KeysFunc,
	isZhipuProvider func(string) bool,
) *ZAIQuotaHandler {
	return &ZAIQuotaHandler{
		getProviders:     getProviders,
		getProviderPools: getPools,
		getProviderKeys:  getKeys,
		isZhipuProvider:  isZhipuProvider,
		client:           &http.Client{Timeout: 8 * time.Second},
	}
}

// NewZAIQuotaHandler creates a static Z.AI quota handler.
func NewZAIQuotaHandler(
	providerList []providers.Provider,
	pools map[string]*keypool.KeyPool,
	keys map[string]string,
	isZhipuProvider func(string) bool,
) *ZAIQuotaHandler {
	return &ZAIQuotaHandler{
		providers:       providerList,
		providerPools:   pools,
		providerKeys:    keys,
		isZhipuProvider: isZhipuProvider,
		client:          &http.Client{Timeout: 8 * time.Second},
	}
}

func (h *ZAIQuotaHandler) providerList() []providers.Provider {
	if h.getProviders != nil {
		return h.getProviders()
	}
	return h.providers
}

func (h *ZAIQuotaHandler) poolMap() map[string]*keypool.KeyPool {
	if h.getProviderPools != nil {
		return h.getProviderPools()
	}
	return h.providerPools
}

func (h *ZAIQuotaHandler) keyMap() map[string]string {
	if h.getProviderKeys != nil {
		return h.getProviderKeys()
	}
	return h.providerKeys
}

// ServeHTTP handles GET /v1/providers/{provider}/quota.
func (h *ZAIQuotaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	provider, ok := h.findProvider(providerName)
	if !ok {
		WriteError(w, http.StatusNotFound, "not_found_error", fmt.Sprintf("provider %q not found", providerName))
		return
	}
	if !h.isZhipu(provider.Name(), provider) {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "quota lookup requires zhipu: true")
		return
	}

	apiKey, keyID, err := h.resolveAPIKey(provider.Name(), r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	quota, err := providers.QueryZAIQuota(ctx, h.client, provider.BaseURL(), apiKey)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"object":   "zai_quota",
		"provider": provider.Name(),
		"key_id":   keyID,
		"quota":    quota,
	})
}

func (h *ZAIQuotaHandler) isZhipu(name string, provider providers.Provider) bool {
	if h.isZhipuProvider != nil {
		return h.isZhipuProvider(name)
	}
	return provider.Owner() == providers.ZAIOwner
}

func (h *ZAIQuotaHandler) findProvider(name string) (providers.Provider, bool) {
	for _, provider := range h.providerList() {
		if provider.Name() == name {
			return provider, true
		}
	}
	return nil, false
}

func (h *ZAIQuotaHandler) resolveAPIKey(providerName string, r *http.Request) (apiKey, keyID string, err error) {
	if pool, ok := h.poolMap()[providerName]; ok && pool != nil {
		return resolvePoolAPIKey(pool, r)
	}

	key := h.keyMap()[providerName]
	if key == "" {
		return "", "", fmt.Errorf("no API key configured for provider %q", providerName)
	}
	return key, "default", nil
}

func resolvePoolAPIKey(pool *keypool.KeyPool, r *http.Request) (apiKey, keyID string, err error) {
	keys := pool.Keys()
	if rawIndex := r.URL.Query().Get("key_index"); rawIndex != "" {
		idx, parseErr := strconv.Atoi(rawIndex)
		if parseErr != nil || idx < 0 || idx >= len(keys) {
			return "", "", fmt.Errorf("invalid key_index %q", rawIndex)
		}
		key := keys[idx]
		return key.APIKey, key.ID, nil
	}

	requestedID := r.URL.Query().Get("key_id")
	if requestedID == "" {
		if len(keys) == 1 {
			key := keys[0]
			return key.APIKey, key.ID, nil
		}
		return "", "", fmt.Errorf("key_index or key_id is required for pooled providers")
	}

	for _, key := range keys {
		if key.ID == requestedID {
			return key.APIKey, key.ID, nil
		}
	}
	return "", "", fmt.Errorf("key_id %q not found", requestedID)
}
