package router

import (
	"context"
	"sort"
)

// PriorityWeightedFailoverRouter selects providers using a two-level strategy:
//
//  1. Group healthy providers by Priority (descending).
//  2. Within the highest-priority group, pick one provider using weighted random selection.
//  3. If all providers in the highest group are unavailable, fail over to the next group.
//
// This gives deterministic tier ordering (priority) with probabilistic load distribution
// within each tier (weight).
type PriorityWeightedFailoverRouter struct{}

// NewPriorityWeightedFailoverRouter creates a new priority-weighted-failover router.
func NewPriorityWeightedFailoverRouter() *PriorityWeightedFailoverRouter {
	return &PriorityWeightedFailoverRouter{}
}

// Select chooses a provider from the highest-priority tier using weighted random selection.
// Falls back to lower-priority tiers if the current tier has no available providers.
func (r *PriorityWeightedFailoverRouter) Select(_ context.Context, providers []ProviderInfo) (ProviderInfo, error) {
	if len(providers) == 0 {
		return ProviderInfo{}, ErrNoProviders
	}

	healthy := FilterHealthy(providers)
	if len(healthy) == 0 {
		return ProviderInfo{}, ErrAllProvidersUnhealthy
	}

	groups := groupByPriorityDesc(healthy)

	for _, group := range groups {
		selected, err := weightedRandomSelect(group)
		if err == nil {
			return selected, nil
		}
	}

	return ProviderInfo{}, ErrAllProvidersUnhealthy
}

// Name returns the strategy name.
func (r *PriorityWeightedFailoverRouter) Name() string {
	return StrategyPriorityWeightedFailover
}

// groupByPriorityDesc groups providers by priority into slices ordered from highest to lowest.
func groupByPriorityDesc(providers []ProviderInfo) [][]ProviderInfo {
	if len(providers) == 0 {
		return nil
	}

	tiers := make(map[int][]ProviderInfo)
	for _, p := range providers {
		tiers[p.Priority] = append(tiers[p.Priority], p)
	}

	levels := make([]int, 0, len(tiers))
	for level := range tiers {
		levels = append(levels, level)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(levels)))

	groups := make([][]ProviderInfo, 0, len(levels))
	for _, level := range levels {
		groups = append(groups, tiers[level])
	}
	return groups
}

// weightedRandomSelect picks one provider from the slice using weighted random selection.
// Each provider's effective weight is used (minimum 1). Returns ErrAllProvidersUnhealthy
// if the slice is empty.
func weightedRandomSelect(providers []ProviderInfo) (ProviderInfo, error) {
	if len(providers) == 0 {
		return ProviderInfo{}, ErrAllProvidersUnhealthy
	}

	if len(providers) == 1 {
		return providers[0], nil
	}

	totalWeight := 0
	for _, p := range providers {
		totalWeight += providerEffectiveWeight(p.Weight)
	}

	if totalWeight <= 0 {
		return providers[0], nil
	}

	roll := randIntn(totalWeight)
	for _, p := range providers {
		w := providerEffectiveWeight(p.Weight)
		if roll < w {
			return p, nil
		}
		roll -= w
	}

	return providers[len(providers)-1], nil
}

func providerEffectiveWeight(w int) int {
	if w <= 0 {
		return 1
	}
	return w
}
