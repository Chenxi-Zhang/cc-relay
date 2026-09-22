package di

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/router"
)

func TestResponsesProviderInfosFiltersLiveAllowlist(t *testing.T) {
	t.Parallel()

	chatProvider := providers.NewOpenAIProviderWithMapping(
		"chat-only", "https://chat.example.test/v1", nil, nil,
	)
	responsesProvider := providers.NewOpenAIProviderWithMapping(
		"openai-main", "https://responses.example.test/v1", nil, nil,
	)
	secondResponsesProvider := providers.NewOpenAIProviderWithMapping(
		"openai-eu", "https://eu.example.test/v1", nil, nil,
	)
	infos := []router.ProviderInfo{
		{Provider: chatProvider},
		{Provider: responsesProvider},
		{Provider: secondResponsesProvider},
	}

	cfg := &config.Config{
		Responses: config.ResponsesConfig{
			ProviderNames: []string{"openai-main"},
		},
	}
	cfgSvc := NewConfigServiceWithConfig(cfg)
	chatGetter := func() []router.ProviderInfo { return infos }
	responsesGetter := responsesProviderInfos(chatGetter, cfgSvc)

	assert.Equal(t, []string{"chat-only", "openai-main", "openai-eu"}, providerNames(chatGetter()))
	assert.Equal(t, []string{"openai-main"}, providerNames(responsesGetter()))

	updated := &config.Config{
		Responses: config.ResponsesConfig{
			ProviderNames: []string{"openai-main", "openai-eu"},
		},
	}
	cfgSvc.GetConfigAtomic().Store(updated)

	assert.Equal(t, []string{"openai-main", "openai-eu"}, providerNames(responsesGetter()))
	assert.Equal(t, []string{"chat-only", "openai-main", "openai-eu"}, providerNames(chatGetter()))
}

func TestResponsesProviderInfosEmptyAndUnknownAllowlists(t *testing.T) {
	t.Parallel()

	first := providers.NewOpenAIProviderWithMapping(
		"first", "https://first.example.test/v1", nil, nil,
	)
	second := providers.NewOpenAIProviderWithMapping(
		"second", "https://second.example.test/v1", nil, nil,
	)
	infos := []router.ProviderInfo{{Provider: first}, {Provider: second}}

	emptyGetter := responsesProviderInfos(
		func() []router.ProviderInfo { return infos },
		NewConfigServiceWithConfig(&config.Config{}),
	)
	assert.Equal(t, []string{"first", "second"}, providerNames(emptyGetter()))

	unknownGetter := responsesProviderInfos(
		func() []router.ProviderInfo { return infos },
		NewConfigServiceWithConfig(&config.Config{
			Responses: config.ResponsesConfig{ProviderNames: []string{"missing"}},
		}),
	)
	require.Empty(t, unknownGetter())
}

func providerNames(infos []router.ProviderInfo) []string {
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Provider.Name())
	}
	return names
}
