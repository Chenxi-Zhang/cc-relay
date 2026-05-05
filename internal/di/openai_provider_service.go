package di

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/samber/do/v2"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/providers"
)

// OpenAIProviderMapService wraps the map of OpenAI providers with hot-reload support.
type OpenAIProviderMapService struct {
	data   atomic.Pointer[providerMapData]
	cfgSvc *ConfigService

	Providers    map[string]providers.Provider
	AllProviders []providers.Provider
	PrimaryKey   string
}

// GetProviders returns the current OpenAI provider map (live, hot-reload aware).
func (s *OpenAIProviderMapService) GetProviders() map[string]providers.Provider {
	d := s.data.Load()
	if d == nil {
		return s.Providers
	}
	return d.Providers
}

// GetAllProviders returns the current all OpenAI providers slice (live, hot-reload aware).
func (s *OpenAIProviderMapService) GetAllProviders() []providers.Provider {
	d := s.data.Load()
	if d == nil {
		return s.AllProviders
	}
	return d.AllProviders
}

// GetPrimaryKey returns the first key from the first enabled OpenAI provider.
func (s *OpenAIProviderMapService) GetPrimaryKey() string {
	d := s.data.Load()
	if d == nil {
		return s.PrimaryKey
	}
	return d.PrimaryKey
}

// RebuildFrom rebuilds the OpenAI provider map from the given config.
func (s *OpenAIProviderMapService) RebuildFrom(cfg *config.Config) error {
	ctx := context.Background()

	providerMap := make(map[string]providers.Provider)
	var allProviders []providers.Provider
	var primaryKey string

	for idx := range cfg.OpenAIProviders {
		providerCfg := &cfg.OpenAIProviders[idx]
		if !providerCfg.Enabled {
			continue
		}

		prov, err := createProvider(ctx, providerCfg)
		if errors.Is(err, ErrUnknownProviderType) {
			log.Warn().
				Str("provider", providerCfg.Name).
				Str("type", providerCfg.Type).
				Msg("skipping unknown openai provider type on reload")
			continue
		}
		if err != nil {
			log.Error().Err(err).Str("provider", providerCfg.Name).Msg("failed to create openai provider on reload")
			continue
		}

		providerMap[providerCfg.Name] = prov
		allProviders = append(allProviders, prov)

		if primaryKey == "" {
			for _, keyCfg := range providerCfg.Keys {
				if keyCfg.IsEnabled() {
					primaryKey = keyCfg.Key
					break
				}
			}
		}
	}

	if len(providerMap) == 0 {
		log.Warn().Msg("no enabled openai providers in new config, keeping current providers")
		return nil
	}

	s.data.Store(&providerMapData{
		Providers:    providerMap,
		AllProviders: allProviders,
		PrimaryKey:   primaryKey,
	})
	s.Providers = providerMap
	s.AllProviders = allProviders
	s.PrimaryKey = primaryKey

	return nil
}

// StartWatching begins watching config changes for OpenAI provider updates.
func (s *OpenAIProviderMapService) StartWatching() {
	if s.cfgSvc == nil || s.cfgSvc.watcher == nil {
		return
	}

	s.cfgSvc.watcher.OnReload(func(newCfg *config.Config) error {
		if err := s.RebuildFrom(newCfg); err != nil {
			log.Error().Err(err).Msg("failed to rebuild openai provider map after config reload")
		}
		log.Info().Msg("openai provider map rebuilt after config reload")
		return nil
	})
}

// NewOpenAIProviderMap creates the map of enabled OpenAI providers with hot-reload support.
func NewOpenAIProviderMap(i do.Injector) (*OpenAIProviderMapService, error) {
	cfgSvc := do.MustInvoke[*ConfigService](i)
	cfg := cfgSvc.Config

	svc := &OpenAIProviderMapService{
		data:         atomic.Pointer[providerMapData]{},
		cfgSvc:       cfgSvc,
		Providers:    make(map[string]providers.Provider),
		AllProviders: nil,
		PrimaryKey:   "",
	}

	ctx := context.Background()

	for idx := range cfg.OpenAIProviders {
		providerCfg := &cfg.OpenAIProviders[idx]
		if !providerCfg.Enabled {
			continue
		}

		prov, err := createProvider(ctx, providerCfg)
		if errors.Is(err, ErrUnknownProviderType) {
			continue
		}
		if err != nil {
			return nil, err
		}

		svc.Providers[providerCfg.Name] = prov
		svc.AllProviders = append(svc.AllProviders, prov)

		if svc.PrimaryKey == "" {
			for _, keyCfg := range providerCfg.Keys {
				if keyCfg.IsEnabled() {
					svc.PrimaryKey = keyCfg.Key
					break
				}
			}
		}
	}

	if len(svc.Providers) == 0 {
		log.Info().Msg("no openai providers configured, openai routes will be unavailable")
	}

	svc.data.Store(&providerMapData{
		Providers:    svc.Providers,
		AllProviders: svc.AllProviders,
		PrimaryKey:   svc.PrimaryKey,
	})

	svc.StartWatching()

	return svc, nil
}
