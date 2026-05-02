package keypool

import (
	"errors"
	"fmt"
	"sort"
)

// PriorityTierSelector wraps a KeySelector to add priority-tiered selection.
// Keys are grouped by Priority (descending). The inner selector is called
// with only the keys in the highest-priority tier. If all keys in that tier
// are unavailable (inner returns ErrAllKeysExhausted), the next tier is tried.
type PriorityTierSelector struct {
	inner KeySelector
}

// NewPriorityTierSelector creates a new priority-tiered selector that
// delegates within each tier to the provided inner selector.
func NewPriorityTierSelector(inner KeySelector) *PriorityTierSelector {
	return &PriorityTierSelector{inner: inner}
}

// Select chooses a key using priority-tiered selection.
//
// Algorithm:
//  1. If no keys → return ErrNoKeys
//  2. Group keys by Priority value
//  3. Sort priority levels descending (highest first)
//  4. For each tier, call inner.Select with only that tier's keys
//  5. If inner returns a key → return it
//  6. If inner returns ErrAllKeysExhausted → try next tier
//  7. If all tiers exhausted → return ErrAllKeysExhausted
func (s *PriorityTierSelector) Select(keys []*KeyMetadata) (*KeyMetadata, error) {
	if len(keys) == 0 {
		return nil, ErrNoKeys
	}

	tiers := make(map[int][]*KeyMetadata)
	for _, key := range keys {
		p := key.Priority
		tiers[p] = append(tiers[p], key)
	}

	levels := make([]int, 0, len(tiers))
	for level := range tiers {
		levels = append(levels, level)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(levels)))

	for _, level := range levels {
		key, err := s.inner.Select(tiers[level])
		if err == nil {
			return key, nil
		}
		if errors.Is(err, ErrAllKeysExhausted) {
			continue
		}
		return nil, err
	}

	return nil, ErrAllKeysExhausted
}

// Name returns the strategy name including the inner selector name.
func (s *PriorityTierSelector) Name() string {
	return fmt.Sprintf("priority_tier(%s)", s.inner.Name())
}
