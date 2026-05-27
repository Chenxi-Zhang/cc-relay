package di

import (
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/samber/do/v2"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/router"
)

// ProviderInfoService holds live provider routing information with atomic swap support.
// Provider info (enabled/disabled, weights, priorities) is rebuilt on config reload
// and atomically swapped for thread-safe access without mutex overhead.
type ProviderInfoService struct {
	// infos holds the current provider info slice via atomic pointer
	infos atomic.Pointer[[]router.ProviderInfo]

	// cfgSvc provides access to current config for rebuilding
	cfgSvc *ConfigService

	// providerSvc gives access to provider instances
	providerSvc *ProviderMapService

	// trackerSvc provides health check functions
	trackerSvc *HealthTrackerService
}

// Get returns the current provider info slice (lock-free read).
// Returns a shallow copy to prevent callers from mutating the internal slice.
func (s *ProviderInfoService) Get() []router.ProviderInfo {
	ptr := s.infos.Load()
	if ptr == nil {
		return nil
	}
	// Return shallow copy (append to nil) to prevent mutation of internal slice
	return append([]router.ProviderInfo(nil), (*ptr)...)
}

// Rebuild rebuilds the provider info slice from current config.
// This should be called on config reload to update provider routing inputs.
func (s *ProviderInfoService) Rebuild() {
	cfg := s.cfgSvc.Get()
	s.RebuildFrom(cfg)
}

// RebuildFrom rebuilds the provider info slice from the given config.
func (s *ProviderInfoService) RebuildFrom(cfg *config.Config) {
	infos := rebuildProviderInfos(cfg.Providers, s.providerSvc.GetProviders(), s.trackerSvc)
	s.infos.Store(&infos)
}

// StartWatching begins watching config changes for provider info updates.
// Registers a callback with the config watcher to rebuild provider info on reload.
func (s *ProviderInfoService) StartWatching() {
	if s.cfgSvc.watcher == nil {
		return
	}

	// Register callback to rebuild provider info on config reload.
	// Important: We rebuild from the newCfg passed to the callback, not from
	// cfgSvc.Get(), to ensure we use the freshly loaded config regardless of
	// callback registration order.
	s.cfgSvc.watcher.OnReload(func(newCfg *config.Config) error {
		s.RebuildFrom(newCfg)
		log.Info().Msg("provider info rebuilt after config reload")
		return nil
	})
}

// NewProviderInfo creates the provider info service with hot-reload support.
// Provider info (enabled/disabled, weights, priorities) is rebuilt on config reload.
func NewProviderInfo(i do.Injector) (*ProviderInfoService, error) {
	cfgSvc := do.MustInvoke[*ConfigService](i)
	providerSvc := do.MustInvoke[*ProviderMapService](i)
	trackerSvc := do.MustInvoke[*HealthTrackerService](i)

	svc := &ProviderInfoService{
		infos:       atomic.Pointer[[]router.ProviderInfo]{},
		cfgSvc:      cfgSvc,
		providerSvc: providerSvc,
		trackerSvc:  trackerSvc,
	}

	// Build initial provider info
	svc.Rebuild()

	// Start watching for config changes
	svc.StartWatching()

	return svc, nil
}

type providerLookup interface {
	GetProviders() map[string]providers.Provider
}

func rebuildProviderInfos(
	providerCfgs []config.ProviderConfig,
	providerMap map[string]providers.Provider,
	trackerSvc *HealthTrackerService,
) []router.ProviderInfo {
	var infos []router.ProviderInfo
	for idx := range providerCfgs {
		cfg := &providerCfgs[idx]
		if !cfg.Enabled {
			continue
		}
		prov, ok := providerMap[cfg.Name]
		if !ok {
			continue
		}
		infos = append(infos, router.ProviderInfo{
			Provider:  prov,
			Weight:    cfg.GetEffectiveWeight(),
			Priority:  cfg.GetEffectivePriority(),
			IsHealthy: trackerSvc.Tracker.IsHealthyFunc(cfg.Name),
		})
	}
	return infos
}
