// Package proxy implements the HTTP proxy server for cc-relay.
package proxy

import (
	"net/http"
	"time"

	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/samber/lo"
)

// KeyStatus represents the runtime status of a single API key.
type KeyStatus struct {
	ID              string `json:"id"`
	Available       bool   `json:"available"`
	Healthy         bool   `json:"healthy"`
	CooldownSeconds int    `json:"cooldown_seconds"`
	RPMRemaining    int    `json:"rpm_remaining"`
	RPMLimit        int    `json:"rpm_limit"`
}

// ProviderInfo represents provider information in the API response.
type ProviderInfo struct {
	Name          string      `json:"name"`
	Type          string      `json:"type"`
	BaseURL       string      `json:"base_url"`
	Models        []string    `json:"models"`
	Active        bool        `json:"active"`
	Keys          []KeyStatus `json:"keys,omitempty"`
	KeysAvailable int         `json:"keys_available"`
	KeysTotal     int         `json:"keys_total"`
}

// ProvidersResponse represents the response format for /v1/providers endpoint.
type ProvidersResponse struct {
	Object string         `json:"object"`
	Data   []ProviderInfo `json:"data"`
}

// ProvidersHandler handles requests to /v1/providers endpoint.
type ProvidersHandler struct {
	getProviders     ProvidersGetter
	providers        []providers.Provider
	getProviderPools func() map[string]*keypool.KeyPool
	providerPools    map[string]*keypool.KeyPool
}

// NewProvidersHandler creates a new providers handler with the given providers.
func NewProvidersHandler(providerList []providers.Provider) *ProvidersHandler {
	return &ProvidersHandler{
		getProviders: nil,
		providers:    providerList,
	}
}

// NewProvidersHandlerWithProviderFunc creates a providers handler with a live provider accessor.
func NewProvidersHandlerWithProviderFunc(getProviders ProvidersGetter) *ProvidersHandler {
	return &ProvidersHandler{
		getProviders: getProviders,
		providers:    nil,
	}
}

// NewProvidersHandlerWithPools creates a providers handler with static provider pools.
func NewProvidersHandlerWithPools(providerList []providers.Provider, pools map[string]*keypool.KeyPool) *ProvidersHandler {
	return &ProvidersHandler{
		getProviders:  nil,
		providers:     providerList,
		providerPools: pools,
	}
}

// NewProvidersHandlerWithProviderFuncAndPools creates a providers handler with live accessors
// for both providers and pools, enabling hot-reloadable key status in the response.
func NewProvidersHandlerWithProviderFuncAndPools(getProviders ProvidersGetter, getPools func() map[string]*keypool.KeyPool) *ProvidersHandler {
	return &ProvidersHandler{
		getProviders:     getProviders,
		providers:        nil,
		getProviderPools: getPools,
	}
}

func (h *ProvidersHandler) providerList() []providers.Provider {
	if h.getProviders != nil {
		return h.getProviders()
	}
	return h.providers
}

func (h *ProvidersHandler) poolMap() map[string]*keypool.KeyPool {
	if h.getProviderPools != nil {
		return h.getProviderPools()
	}
	return h.providerPools
}

// ServeHTTP handles GET /v1/providers requests.
func (h *ProvidersHandler) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	pools := h.poolMap()

	// Collect provider information using lo.Map
	data := lo.Map(h.providerList(), func(provider providers.Provider, _ int) ProviderInfo {
		// Extract model IDs from provider's models using lo.Map
		modelIDs := lo.Map(provider.ListModels(), func(m providers.Model, _ int) string {
			return m.ID
		})

		info := ProviderInfo{
			Name:    provider.Name(),
			Type:    provider.Owner(),
			BaseURL: provider.BaseURL(),
			Models:  modelIDs,
			Active:  true,
		}

		// Populate key runtime status if pool exists for this provider
		if pool, ok := pools[provider.Name()]; ok && pool != nil {
			keys := pool.Keys()
			info.KeysTotal = len(keys)
			info.Keys = make([]KeyStatus, 0, len(keys))

			for _, key := range keys {
				snap := key.Snapshot()

				cooldownSec := 0
				if !snap.CooldownUntil.IsZero() {
					if remaining := time.Until(snap.CooldownUntil); remaining > 0 {
						cooldownSec = int(remaining.Seconds())
					}
				}

				if snap.Available {
					info.KeysAvailable++
				}

				info.Keys = append(info.Keys, KeyStatus{
					ID:              snap.ID,
					Available:       snap.Available,
					Healthy:         snap.Healthy,
					CooldownSeconds: cooldownSec,
					RPMRemaining:    snap.RPMRemaining,
					RPMLimit:        snap.RPMLimit,
				})
			}
		}

		return info
	})

	response := ProvidersResponse{
		Object: "list",
		Data:   data,
	}

	writeJSON(writer, http.StatusOK, response)
}
