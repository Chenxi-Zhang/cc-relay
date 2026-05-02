package keypool_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPriorityTierSelectsHighestPriority(t *testing.T) {
	t.Parallel()
	selector := keypool.NewPriorityTierSelector(keypool.NewLeastLoadedSelector())

	keys := []*keypool.KeyMetadata{
		keypool.NewKeyMetadata(0, "low-key", 50, 30000, 30000),
		keypool.NewKeyMetadata(1, "normal-key", 50, 30000, 30000),
		keypool.NewKeyMetadata(2, "high-key", 50, 30000, 30000),
	}
	keys[0].Priority = 0
	keys[1].Priority = 1
	keys[2].Priority = 2

	selected, err := selector.Select(keys)
	require.NoError(t, err)
	assert.Equal(t, "high-key", selected.APIKey, "should select highest priority key")
}

func TestPriorityTierFallsThroughOnExhaustion(t *testing.T) {
	t.Parallel()
	selector := keypool.NewPriorityTierSelector(keypool.NewLeastLoadedSelector())

	keys := []*keypool.KeyMetadata{
		keypool.NewKeyMetadata(0, "high-key", 50, 30000, 30000),
		keypool.NewKeyMetadata(1, "normal-key", 50, 30000, 30000),
	}
	keys[0].Priority = 2
	keys[1].Priority = 1

	keys[0].MarkUnhealthy(errors.New("exhausted"))

	selected, err := selector.Select(keys)
	require.NoError(t, err)
	assert.Equal(t, "normal-key", selected.APIKey, "should fall through to next tier")
}

func TestPriorityTierAllExhausted(t *testing.T) {
	t.Parallel()
	selector := keypool.NewPriorityTierSelector(keypool.NewLeastLoadedSelector())

	keys := []*keypool.KeyMetadata{
		keypool.NewKeyMetadata(0, "high-key", 50, 30000, 30000),
		keypool.NewKeyMetadata(1, "low-key", 50, 30000, 30000),
	}
	keys[0].Priority = 2
	keys[1].Priority = 0
	keys[0].MarkUnhealthy(errors.New("down"))
	keys[1].MarkUnhealthy(errors.New("down"))

	_, err := selector.Select(keys)
	assert.ErrorIs(t, err, keypool.ErrAllKeysExhausted)
}

func TestPriorityTierNoKeys(t *testing.T) {
	t.Parallel()
	selector := keypool.NewPriorityTierSelector(keypool.NewLeastLoadedSelector())

	_, err := selector.Select([]*keypool.KeyMetadata{})
	assert.ErrorIs(t, err, keypool.ErrNoKeys)
}

func TestPriorityTierSingleTier(t *testing.T) {
	t.Parallel()
	selector := keypool.NewPriorityTierSelector(keypool.NewLeastLoadedSelector())

	keys := []*keypool.KeyMetadata{
		keypool.NewKeyMetadata(0, "key1", 50, 30000, 30000),
		keypool.NewKeyMetadata(1, "key2", 50, 30000, 30000),
	}
	keys[0].Priority = 1
	keys[1].Priority = 1
	keys[0].RPMRemaining = 10

	selected, err := selector.Select(keys)
	require.NoError(t, err)
	assert.Equal(t, "key2", selected.APIKey, "with same priority, least_loaded picks highest capacity")
}

func TestPriorityTierWithAllStrategies(t *testing.T) {
	t.Parallel()
	strategies := map[string]keypool.KeySelector{
		"least_loaded": keypool.NewLeastLoadedSelector(),
		"round_robin":  keypool.NewRoundRobinSelector(),
		"weighted":     keypool.NewWeightedSelector(),
		"random":       keypool.NewRandomSelector(),
	}

	for name, inner := range strategies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			selector := keypool.NewPriorityTierSelector(inner)

			keys := []*keypool.KeyMetadata{
				keypool.NewKeyMetadata(0, "low-key", 50, 30000, 30000),
				keypool.NewKeyMetadata(1, "high-key", 50, 30000, 30000),
			}
			keys[0].Priority = 0
			keys[1].Priority = 2

			selected, err := selector.Select(keys)
			require.NoError(t, err)
			assert.Equal(t, "high-key", selected.APIKey, "%s inner should select high priority key", name)
		})
	}
}

func TestPriorityTierGroupsSamePriority(t *testing.T) {
	t.Parallel()
	selector := keypool.NewPriorityTierSelector(keypool.NewLeastLoadedSelector())

	keys := []*keypool.KeyMetadata{
		keypool.NewKeyMetadata(0, "high1", 50, 30000, 30000),
		keypool.NewKeyMetadata(1, "high2", 50, 30000, 30000),
		keypool.NewKeyMetadata(2, "low1", 50, 30000, 30000),
	}
	keys[0].Priority = 2
	keys[1].Priority = 2
	keys[2].Priority = 0

	keys[0].RPMRemaining = 10
	keys[1].RPMRemaining = 50

	selected, err := selector.Select(keys)
	require.NoError(t, err)
	assert.Equal(t, "high2", selected.APIKey, "both high-priority keys are in same tier, least_loaded picks best")
}

func TestPriorityTierName(t *testing.T) {
	t.Parallel()

	ll := keypool.NewPriorityTierSelector(keypool.NewLeastLoadedSelector())
	assert.Equal(t, "priority_tier(least_loaded)", ll.Name())

	rr := keypool.NewPriorityTierSelector(keypool.NewRoundRobinSelector())
	assert.Equal(t, "priority_tier(round_robin)", rr.Name())
}

func TestPriorityTierConcurrentSafety(t *testing.T) {
	t.Parallel()
	selector := keypool.NewPriorityTierSelector(keypool.NewRoundRobinSelector())

	keys := []*keypool.KeyMetadata{
		keypool.NewKeyMetadata(0, "high1", 50, 30000, 30000),
		keypool.NewKeyMetadata(1, "high2", 50, 30000, 30000),
		keypool.NewKeyMetadata(2, "low1", 50, 30000, 30000),
	}
	keys[0].Priority = 2
	keys[1].Priority = 2
	keys[2].Priority = 0

	numGoroutines := 100
	selectionsPerGoroutine := 20

	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	for range numGoroutines {
		go func() {
			defer wg.Done()
			for range selectionsPerGoroutine {
				_, err := selector.Select(keys)
				if err != nil {
					t.Errorf("Concurrent Select() error = %v", err)
				}
			}
		}()
	}
	wg.Wait()
}
