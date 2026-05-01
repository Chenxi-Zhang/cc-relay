package di

import (
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/samber/do/v2"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/keypool"
)

// OpenAIKeyPoolMapService wraps per-provider key pools for OpenAI providers.
type OpenAIKeyPoolMapService struct {
	data   atomic.Pointer[keyPoolMapData]
	cfgSvc *ConfigService

	Pools map[string]*keypool.KeyPool
	Keys  map[string]string
}

// GetPools returns the current OpenAI key pools (live, hot-reload aware).
func (s *OpenAIKeyPoolMapService) GetPools() map[string]*keypool.KeyPool {
	d := s.data.Load()
	if d == nil {
		return s.Pools
	}
	return d.Pools
}

// GetKeys returns the current fallback keys (live, hot-reload aware).
func (s *OpenAIKeyPoolMapService) GetKeys() map[string]string {
	d := s.data.Load()
	if d == nil {
		return s.Keys
	}
	return d.Keys
}

// RebuildFrom rebuilds key pools from OpenAI providers in the given config.
func (s *OpenAIKeyPoolMapService) RebuildFrom(cfg *config.Config) error {
	pools := make(map[string]*keypool.KeyPool)
	keys := make(map[string]string)

	for idx := range cfg.OpenAIProviders {
		providerCfg := &cfg.OpenAIProviders[idx]
		if !providerCfg.Enabled {
			continue
		}

		if len(providerCfg.Keys) > 0 {
			keys[providerCfg.Name] = providerCfg.Keys[0].Key
		}

		if !providerCfg.IsPoolingEnabled() {
			continue
		}

		poolCfg := buildPoolConfig(providerCfg)

		pool, err := keypool.NewKeyPool(providerCfg.Name, poolCfg)
		if err != nil {
			log.Error().Err(err).Str("provider", providerCfg.Name).Msg("failed to create openai key pool on reload")
			continue
		}

		pools[providerCfg.Name] = pool
	}

	s.data.Store(&keyPoolMapData{Pools: pools, Keys: keys})
	s.Pools = pools
	s.Keys = keys

	return nil
}

// StartWatching begins watching config changes for OpenAI key pool updates.
func (s *OpenAIKeyPoolMapService) StartWatching() {
	watchConfig(
		s.cfgSvc,
		s.RebuildFrom,
		"failed to rebuild openai key pools after config reload",
		"openai key pools rebuilt after config reload",
		true,
	)
}

// NewOpenAIKeyPoolMap creates key pools for all enabled OpenAI providers.
func NewOpenAIKeyPoolMap(i do.Injector) (*OpenAIKeyPoolMapService, error) {
	cfgSvc := do.MustInvoke[*ConfigService](i)
	svc := &OpenAIKeyPoolMapService{
		cfgSvc: cfgSvc,
		data:   atomic.Pointer[keyPoolMapData]{},
		Pools:  nil,
		Keys:   nil,
	}
	if err := initKeyPoolService(cfgSvc, svc.RebuildFrom, svc.StartWatching); err != nil {
		return nil, err
	}

	return svc, nil
}
