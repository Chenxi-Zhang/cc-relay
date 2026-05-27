package di

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/samber/do/v2"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/providers"
)

// providerMapData holds the provider map data for atomic swap.
type providerMapData struct {
	PrimaryProvider providers.Provider
	Providers       map[string]providers.Provider
	PrimaryKey      string
	AllProviders    []providers.Provider
}

// ProviderMapService wraps the map of providers with hot-reload support.
// Providers are rebuilt on config reload to support enabling/disabling providers dynamically.
type ProviderMapService struct {
	data   atomic.Pointer[providerMapData]
	cfgSvc *ConfigService

	// For backward compatibility
	PrimaryProvider providers.Provider
	Providers       map[string]providers.Provider
	PrimaryKey      string
	AllProviders    []providers.Provider
}

// GetPrimaryProvider returns the current primary provider (live, hot-reload aware).
func (s *ProviderMapService) GetPrimaryProvider() providers.Provider {
	d := s.data.Load()
	if d == nil {
		return s.PrimaryProvider
	}
	return d.PrimaryProvider
}

// GetPrimaryKey returns the current primary provider key (live, hot-reload aware).
func (s *ProviderMapService) GetPrimaryKey() string {
	d := s.data.Load()
	if d == nil {
		return s.PrimaryKey
	}
	return d.PrimaryKey
}

// GetProviders returns the current provider map (live, hot-reload aware).
func (s *ProviderMapService) GetProviders() map[string]providers.Provider {
	d := s.data.Load()
	if d == nil {
		return s.Providers // Fallback to legacy field
	}
	return d.Providers
}

// GetAllProviders returns the current all providers slice (live, hot-reload aware).
func (s *ProviderMapService) GetAllProviders() []providers.Provider {
	d := s.data.Load()
	if d == nil {
		return s.AllProviders // Fallback to legacy field
	}
	return d.AllProviders
}

// GetProvider returns a provider by name (live, hot-reload aware).
func (s *ProviderMapService) GetProvider(name string) (providers.Provider, bool) {
	providersMap := s.GetProviders()
	if providersMap == nil {
		return nil, false
	}
	prov, ok := providersMap[name]
	return prov, ok
}

// RebuildFrom rebuilds the provider map from the given config.
func (s *ProviderMapService) RebuildFrom(cfg *config.Config) error {
	ctx := context.Background()
	result := rebuildProviderMap(ctx, cfg.Providers)

	if result.PrimaryProvider == nil {
		log.Warn().Msg("no enabled providers in new config, keeping current providers")
		return nil
	}

	s.data.Store(&providerMapData{
		PrimaryProvider: result.PrimaryProvider,
		Providers:       result.ProviderMap,
		PrimaryKey:      result.PrimaryKey,
		AllProviders:    result.AllProviders,
	})
	s.PrimaryProvider = result.PrimaryProvider
	s.Providers = result.ProviderMap
	s.PrimaryKey = result.PrimaryKey
	s.AllProviders = result.AllProviders

	return nil
}

// StartWatching begins watching config changes for provider map updates.
func (s *ProviderMapService) StartWatching() {
	if s.cfgSvc == nil || s.cfgSvc.watcher == nil {
		return
	}

	s.cfgSvc.watcher.OnReload(func(newCfg *config.Config) error {
		if err := s.RebuildFrom(newCfg); err != nil {
			log.Error().Err(err).Msg("failed to rebuild provider map after config reload")
		}
		log.Info().Msg("provider map rebuilt after config reload")
		return nil
	})
}

type providerMapResult struct {
	ProviderMap     map[string]providers.Provider
	AllProviders    []providers.Provider
	PrimaryProvider providers.Provider
	PrimaryKey      string
}

func rebuildProviderMap(ctx context.Context, providerCfgs []config.ProviderConfig) providerMapResult {
	providerMap := make(map[string]providers.Provider)
	var allProviders []providers.Provider
	var primaryProvider providers.Provider
	var primaryKey string

	for idx := range providerCfgs {
		cfg := &providerCfgs[idx]
		if !cfg.Enabled {
			continue
		}

		prov, err := createProvider(ctx, cfg)
		if errors.Is(err, ErrUnknownProviderType) {
			log.Warn().Str("provider", cfg.Name).Str("type", cfg.Type).Msg("skipping unknown provider type on reload")
			continue
		}
		if err != nil {
			log.Error().Err(err).Str("provider", cfg.Name).Msg("failed to create provider on reload")
			continue
		}

		providerMap[cfg.Name] = prov
		allProviders = append(allProviders, prov)

		if primaryProvider == nil {
			primaryProvider = prov
			for _, keyCfg := range cfg.Keys {
				if keyCfg.IsEnabled() {
					primaryKey = keyCfg.Key
					break
				}
			}
		}
	}

	return providerMapResult{
		ProviderMap:     providerMap,
		AllProviders:    allProviders,
		PrimaryProvider: primaryProvider,
		PrimaryKey:      primaryKey,
	}
}

// NewProviderMap creates the map of enabled providers with hot-reload support.
func NewProviderMap(i do.Injector) (*ProviderMapService, error) {
	cfgSvc := do.MustInvoke[*ConfigService](i)
	cfg := cfgSvc.Config

	ctx := context.Background()
	result := rebuildProviderMap(ctx, cfg.Providers)

	if result.PrimaryProvider == nil {
		return nil, fmt.Errorf("no enabled provider found (supported: %s)", supportedProviderTypes)
	}

	svc := &ProviderMapService{
		data:            atomic.Pointer[providerMapData]{},
		cfgSvc:          cfgSvc,
		Providers:       result.ProviderMap,
		PrimaryProvider: result.PrimaryProvider,
		PrimaryKey:      result.PrimaryKey,
		AllProviders:    result.AllProviders,
	}

	svc.data.Store(&providerMapData{
		PrimaryProvider: result.PrimaryProvider,
		Providers:       result.ProviderMap,
		PrimaryKey:      result.PrimaryKey,
		AllProviders:    result.AllProviders,
	})

	svc.StartWatching()

	return svc, nil
}
