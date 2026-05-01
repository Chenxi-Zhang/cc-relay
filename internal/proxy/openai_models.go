package proxy

import (
	"net/http"
	"time"

	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/samber/lo"
)

// OpenAIModelsHandler handles GET /openai/v1/models requests.
// Returns models in OpenAI API format with object "list" and model entries.
type OpenAIModelsHandler struct {
	getProviders ProvidersGetter
	providers    []providers.Provider
}

// NewOpenAIModelsHandler creates a handler with a static provider list.
func NewOpenAIModelsHandler(providerList []providers.Provider) *OpenAIModelsHandler {
	return &OpenAIModelsHandler{
		getProviders: nil,
		providers:    providerList,
	}
}

// NewOpenAIModelsHandlerWithProviderFunc creates a handler with hot-reloadable provider accessor.
func NewOpenAIModelsHandlerWithProviderFunc(getProviders ProvidersGetter) *OpenAIModelsHandler {
	return &OpenAIModelsHandler{
		getProviders: getProviders,
		providers:    nil,
	}
}

func (h *OpenAIModelsHandler) providerList() []providers.Provider {
	if h.getProviders != nil {
		return h.getProviders()
	}
	return h.providers
}

// ServeHTTP handles GET /openai/v1/models requests.
func (h *OpenAIModelsHandler) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	now := time.Now().Unix()

	allModels := lo.FlatMap(h.providerList(), func(provider providers.Provider, _ int) []ModelObject {
		models := provider.ListModels()
		result := make([]ModelObject, len(models))
		for i, m := range models {
			result[i] = ModelObject{
				ID:      m.ID,
				Object:  "model",
				Created: now,
				OwnedBy: provider.Owner(),
			}
		}
		return result
	})

	if allModels == nil {
		allModels = []ModelObject{}
	}

	response := OpenAIModelsResponse{
		Object: "list",
		Data:   allModels,
	}

	writeJSON(writer, http.StatusOK, response)
}
