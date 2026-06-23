package bloom

import (
	"database/sql/driver"
	"errors"

	boom "github.com/tylertreat/BoomFilters"
)

// StableBloomFilter wraps boom.StableBloomFilter via a pointer embed.
// The upstream type must not be copied by value — doing so can produce nil
// pointer panics during database Scan operations. Using a pointer embed
// guarantees safe initialisation and assignment. (Fixes PR #446)
type StableBloomFilter struct {
	*boom.StableBloomFilter
}

const (
	defaultCapacity = 100_000_000
	defaultD        = 2
	defaultFpRate   = 0.001
)

func NewDefaultStableBloomFilter() *StableBloomFilter {
	return &StableBloomFilter{boom.NewStableBloomFilter(defaultCapacity, defaultD, defaultFpRate)}
}

func (s *StableBloomFilter) Scan(value interface{}) error {
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New("invalid type for StableBloomFilter")
	}

	bf := boom.NewStableBloomFilter(0, 0, 0)
	if err := bf.GobDecode(bytes); err != nil {
		return err
	}

	// Assign the pointer directly — avoids unsafe value copy of StableBloomFilter.
	s.StableBloomFilter = bf

	return nil
}

func (s StableBloomFilter) Value() (driver.Value, error) {
	if s.Cells() == 0 {
		//nolint:nilnil
		return nil, nil
	}

	return s.GobEncode()
}
