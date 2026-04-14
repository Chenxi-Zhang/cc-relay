// Package proxy implements the HTTP proxy server for cc-relay.
package proxy

import (
	"net/http"

	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/samber/lo"
)

// ModelsResponse represents the response format for /v1/models endpoint.
// This matches the Anthropic model list response format.
type ModelsResponse struct {
	Data    []providers.Model `json:"data"`
	HasMore bool              `json:"has_more"`
	FirstID *string           `json:"first_id"`
	LastID  *string           `json:"last_id"`
}

// ModelsHandler handles requests to /v1/models endpoint.
type ModelsHandler struct {
	getProviders ProvidersGetter
	providers    []providers.Provider
}

// ProvidersGetter returns the current provider list for live updates.
type ProvidersGetter func() []providers.Provider

// NewModelsHandler creates a new models handler with the given providers.
func NewModelsHandler(providerList []providers.Provider) *ModelsHandler {
	return &ModelsHandler{
		getProviders: nil,
		providers:    providerList,
	}
}

// NewModelsHandlerWithProviderFunc creates a models handler with a live provider accessor.
func NewModelsHandlerWithProviderFunc(getProviders ProvidersGetter) *ModelsHandler {
	return &ModelsHandler{
		getProviders: getProviders,
		providers:    nil,
	}
}

func (h *ModelsHandler) providerList() []providers.Provider {
	if h.getProviders != nil {
		return h.getProviders()
	}
	return h.providers
}

// ServeHTTP handles GET /v1/models requests.
func (h *ModelsHandler) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	// Collect all models from all providers using lo.FlatMap
	allModels := lo.FlatMap(h.providerList(), func(provider providers.Provider, _ int) []providers.Model {
		return provider.ListModels()
	})

	var firstID, lastID *string
	if len(allModels) > 0 {
		firstID = &allModels[0].ID
		lastID = &allModels[len(allModels)-1].ID
	}

	response := ModelsResponse{
		Data:    allModels,
		HasMore: false,
		FirstID: firstID,
		LastID:  lastID,
	}

	writeJSON(writer, http.StatusOK, response)
}
