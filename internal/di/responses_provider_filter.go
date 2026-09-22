package di

import (
	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/router"
)

// responsesProviderInfos returns a live provider-info getter for the native
// Responses route. An empty allowlist preserves every configured provider.
func responsesProviderInfos(
	getInfos func() []router.ProviderInfo,
	runtime config.RuntimeConfigGetter,
) func() []router.ProviderInfo {
	if getInfos == nil {
		return func() []router.ProviderInfo { return nil }
	}

	return func() []router.ProviderInfo {
		infos := getInfos()
		if runtime == nil {
			return infos
		}

		cfg := runtime.Get()
		if cfg == nil || len(cfg.Responses.ProviderNames) == 0 {
			return infos
		}

		allowed := make(map[string]struct{}, len(cfg.Responses.ProviderNames))
		for _, name := range cfg.Responses.ProviderNames {
			allowed[name] = struct{}{}
		}

		filtered := make([]router.ProviderInfo, 0, len(infos))
		for _, info := range infos {
			if _, ok := allowed[info.Provider.Name()]; ok {
				filtered = append(filtered, info)
			}
		}
		return filtered
	}
}
