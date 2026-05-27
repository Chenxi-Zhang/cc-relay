package router

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPriorityWeightedFailover_Name(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	assert.Equal(t, StrategyPriorityWeightedFailover, r.Name())
}

func TestPriorityWeightedFailover_EmptyProviders(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	_, err := r.Select(context.Background(), nil)
	assert.ErrorIs(t, err, ErrNoProviders)
}

func TestPriorityWeightedFailover_AllUnhealthy(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	providers := []ProviderInfo{
		NewTestProviderInfo("a", 1, 1, NeverHealthy()),
	}
	_, err := r.Select(context.Background(), providers)
	assert.ErrorIs(t, err, ErrAllProvidersUnhealthy)
}

func TestPriorityWeightedFailover_SingleProvider(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	providers := []ProviderInfo{
		NewTestProviderInfo("a", 10, 5, AlwaysHealthy()),
	}
	selected, err := r.Select(context.Background(), providers)
	require.NoError(t, err)
	assert.Equal(t, "a", selected.Provider.Name())
}

func TestPriorityWeightedFailover_HighestPriorityPreferred(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	providers := []ProviderInfo{
		NewTestProviderInfo("low", 1, 1, AlwaysHealthy()),
		NewTestProviderInfo("high", 100, 1, AlwaysHealthy()),
		NewTestProviderInfo("mid", 50, 1, AlwaysHealthy()),
	}

	selected, err := r.Select(context.Background(), providers)
	require.NoError(t, err)
	assert.Equal(t, "high", selected.Provider.Name())
}

func TestPriorityWeightedFailover_FailoverToLowerTier(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	providers := []ProviderInfo{
		NewTestProviderInfo("high", 100, 1, NeverHealthy()),
		NewTestProviderInfo("low", 1, 1, AlwaysHealthy()),
	}

	selected, err := r.Select(context.Background(), providers)
	require.NoError(t, err)
	assert.Equal(t, "low", selected.Provider.Name())
}

func TestPriorityWeightedFailover_WeightedDistribution(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	providers := []ProviderInfo{
		NewTestProviderInfo("heavy", 10, 99, AlwaysHealthy()),
		NewTestProviderInfo("light", 10, 1, AlwaysHealthy()),
	}

	counts := make(map[string]int)
	const iterations = 10000
	for range iterations {
		selected, err := r.Select(context.Background(), providers)
		require.NoError(t, err)
		counts[selected.Provider.Name()]++
	}

	assert.Greater(t, counts["heavy"], counts["light"]*10)
	assert.Greater(t, counts["heavy"], iterations/2)
}

func TestPriorityWeightedFailover_SamePrioritySameWeight(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	providers := []ProviderInfo{
		NewTestProviderInfo("a", 10, 1, AlwaysHealthy()),
		NewTestProviderInfo("b", 10, 1, AlwaysHealthy()),
	}

	counts := make(map[string]int)
	const iterations = 10000
	for range iterations {
		selected, err := r.Select(context.Background(), providers)
		require.NoError(t, err)
		counts[selected.Provider.Name()]++
	}

	ratio := float64(counts["a"]) / float64(counts["b"])
	assert.InDelta(t, 1.0, ratio, 0.15)
}

func TestPriorityWeightedFailover_WeightZeroDefaultsToOne(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	providers := []ProviderInfo{
		NewTestProviderInfo("a", 10, 0, AlwaysHealthy()),
		NewTestProviderInfo("b", 10, 0, AlwaysHealthy()),
	}

	counts := make(map[string]int)
	const iterations = 10000
	for range iterations {
		selected, err := r.Select(context.Background(), providers)
		require.NoError(t, err)
		counts[selected.Provider.Name()]++
	}

	ratio := float64(counts["a"]) / float64(counts["b"])
	assert.InDelta(t, 1.0, ratio, 0.15)
}

func TestPriorityWeightedFailover_MultipleTiers(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	providers := []ProviderInfo{
		NewTestProviderInfo("tier2-a", 2, 50, AlwaysHealthy()),
		NewTestProviderInfo("tier2-b", 2, 50, AlwaysHealthy()),
		NewTestProviderInfo("tier1", 1, 1, AlwaysHealthy()),
	}

	counts := make(map[string]int)
	const iterations = 10000
	for range iterations {
		selected, err := r.Select(context.Background(), providers)
		require.NoError(t, err)
		counts[selected.Provider.Name()]++
	}

	assert.Equal(t, 0, counts["tier1"])
	assert.Greater(t, counts["tier2-a"], 0)
	assert.Greater(t, counts["tier2-b"], 0)
}

func TestPriorityWeightedFailover_MultipleTiersFailover(t *testing.T) {
	r := NewPriorityWeightedFailoverRouter()
	providers := []ProviderInfo{
		NewTestProviderInfo("tier3", 3, 1, NeverHealthy()),
		NewTestProviderInfo("tier2", 2, 1, NeverHealthy()),
		NewTestProviderInfo("tier1", 1, 1, AlwaysHealthy()),
	}

	selected, err := r.Select(context.Background(), providers)
	require.NoError(t, err)
	assert.Equal(t, "tier1", selected.Provider.Name())
}

func TestGroupByPriorityDesc(t *testing.T) {
	providers := []ProviderInfo{
		NewTestProviderInfo("a", 1, 1, AlwaysHealthy()),
		NewTestProviderInfo("b", 3, 1, AlwaysHealthy()),
		NewTestProviderInfo("c", 1, 1, AlwaysHealthy()),
		NewTestProviderInfo("d", 2, 1, AlwaysHealthy()),
	}

	groups := groupByPriorityDesc(providers)
	require.Len(t, groups, 3)

	assert.Len(t, groups[0], 1)
	assert.Equal(t, 3, groups[0][0].Priority)

	assert.Len(t, groups[1], 1)
	assert.Equal(t, 2, groups[1][0].Priority)

	assert.Len(t, groups[2], 2)
	assert.Equal(t, 1, groups[2][0].Priority)
	assert.Equal(t, 1, groups[2][1].Priority)
}

func TestWeightedRandomSelect_Empty(t *testing.T) {
	_, err := weightedRandomSelect(nil)
	assert.True(t, errors.Is(err, ErrAllProvidersUnhealthy))
}

func TestWeightedRandomSelect_Single(t *testing.T) {
	providers := []ProviderInfo{
		NewTestProviderInfo("only", 1, 5, AlwaysHealthy()),
	}
	selected, err := weightedRandomSelect(providers)
	require.NoError(t, err)
	assert.Equal(t, "only", selected.Provider.Name())
}

func TestProviderEffectiveWeight(t *testing.T) {
	assert.Equal(t, 1, providerEffectiveWeight(0))
	assert.Equal(t, 1, providerEffectiveWeight(-1))
	assert.Equal(t, 5, providerEffectiveWeight(5))
	assert.Equal(t, 100, providerEffectiveWeight(100))
}
