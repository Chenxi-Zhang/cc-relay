package providers

import (
	"net/http"

	"github.com/rs/zerolog/log"
)

const (
	// GenericOwner is the owner identifier for generic providers.
	GenericOwner = "generic"
)

// GenericProvider implements the Provider interface for any Anthropic-compatible API.
// It requires the user to provide base_url and api_key via configuration.
// Supports configurable authentication: "x-api-key" (default) or "bearer".
type GenericProvider struct {
	BaseProvider
	authMethod string
}

// NewGenericProviderWithMapping creates a new generic provider with model mapping.
// baseURL is required and must not be empty.
// If models is nil, an empty slice is used.
// authMethod controls the authentication header: "x-api-key" (default) or "bearer".
func NewGenericProviderWithMapping(
	name, baseURL string,
	models []string,
	modelMapping map[string]string,
	authMethod string,
) *GenericProvider {
	if models == nil {
		models = []string{}
	}

	return &GenericProvider{
		BaseProvider: NewBaseProviderWithMapping(name, baseURL, GenericOwner, models, modelMapping),
		authMethod:   authMethod,
	}
}

// Authenticate adds authentication to the request based on the configured method.
// "bearer" sets Authorization: Bearer <key>.
// Any other value (including empty) sets x-api-key: <key>.
func (p *GenericProvider) Authenticate(req *http.Request, key string) error {
	if p.authMethod == "bearer" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("x-api-key", key)
	}

	log.Ctx(req.Context()).Debug().
		Str("provider", p.name).
		Str("auth_method", p.authMethod).
		Msg("added authentication header")

	return nil
}
