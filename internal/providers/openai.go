package providers

import (
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	OpenAIOwner = "openai"
)

type OpenAIProvider struct {
	name         string
	baseURL      string
	owner        string
	models       []string
	modelMapping map[string]string
}

func NewOpenAIProviderWithMapping(
	name, baseURL string,
	models []string,
	modelMapping map[string]string,
) *OpenAIProvider {
	if models == nil {
		models = []string{}
	}

	return &OpenAIProvider{
		name:         name,
		baseURL:      baseURL,
		owner:        OpenAIOwner,
		models:       models,
		modelMapping: modelMapping,
	}
}

func (p *OpenAIProvider) Name() string {
	return p.name
}

func (p *OpenAIProvider) BaseURL() string {
	return p.baseURL
}

func (p *OpenAIProvider) Owner() string {
	return p.owner
}

func (p *OpenAIProvider) Authenticate(req *http.Request, key string) error {
	req.Header.Set("Authorization", "Bearer "+key)

	log.Ctx(req.Context()).Debug().
		Str("provider", p.name).
		Msg("added authentication header")

	return nil
}

func (p *OpenAIProvider) ForwardHeaders(_ http.Header) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	return headers
}

func (p *OpenAIProvider) SupportsStreaming() bool {
	return true
}

func (p *OpenAIProvider) SupportsTransparentAuth() bool {
	return false
}

func (p *OpenAIProvider) ListModels() []Model {
	if len(p.models) == 0 {
		return []Model{}
	}

	now := time.Now().UTC().Format(time.RFC3339)

	result := make([]Model, len(p.models))
	for i, modelID := range p.models {
		result[i] = Model{
			ID:          modelID,
			Type:        "model",
			DisplayName: modelID,
			CreatedAt:   now,
		}
	}

	return result
}

func (p *OpenAIProvider) GetModelMapping() map[string]string {
	return p.modelMapping
}

func (p *OpenAIProvider) MapModel(model string) string {
	if p.modelMapping == nil {
		return model
	}
	if mapped, ok := p.modelMapping[model]; ok {
		return mapped
	}
	return model
}

func (p *OpenAIProvider) TransformRequest(body []byte, endpoint string) ([]byte, string, error) {
	return body, p.baseURL + endpoint, nil
}

func (p *OpenAIProvider) TransformResponse(_ *http.Response, _ http.ResponseWriter) error {
	return nil
}

func (p *OpenAIProvider) RequiresBodyTransform() bool {
	return false
}

func (p *OpenAIProvider) StreamingContentType() string {
	return ContentTypeSSE
}
