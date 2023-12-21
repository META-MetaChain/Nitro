// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package das

import (
	"errors"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/META-MetaChain/nitro/METAstate"
)

var ErrNoReadersResponded = errors.New("no DAS readers responded successfully")

type aggregatorStrategy interface {
	newInstance() aggregatorStrategyInstance
	update([]METAstate.DataAvailabilityReader, map[METAstate.DataAvailabilityReader]readerStats)
}

type abstractAggregatorStrategy struct {
	sync.RWMutex
	readers []METAstate.DataAvailabilityReader
	stats   map[METAstate.DataAvailabilityReader]readerStats
}

func (s *abstractAggregatorStrategy) update(readers []METAstate.DataAvailabilityReader, stats map[METAstate.DataAvailabilityReader]readerStats) {
	s.Lock()
	defer s.Unlock()

	s.readers = make([]METAstate.DataAvailabilityReader, len(readers))
	copy(s.readers, readers)

	s.stats = make(map[METAstate.DataAvailabilityReader]readerStats)
	for k, v := range stats {
		s.stats[k] = v
	}
}

// Exponentially growing Explore Exploit Strategy
type simpleExploreExploitStrategy struct {
	iterations        uint32
	exploreIterations uint32
	exploitIterations uint32

	abstractAggregatorStrategy
}

func (s *simpleExploreExploitStrategy) newInstance() aggregatorStrategyInstance {
	iterations := atomic.AddUint32(&s.iterations, 1)

	readerSets := make([][]METAstate.DataAvailabilityReader, 0)
	s.RLock()
	defer s.RUnlock()

	readers := make([]METAstate.DataAvailabilityReader, len(s.readers))
	copy(readers, s.readers)

	if iterations%(s.exploreIterations+s.exploitIterations) < s.exploreIterations {
		// Explore phase
		rand.Shuffle(len(readers), func(i, j int) { readers[i], readers[j] = readers[j], readers[i] })
	} else {
		// Exploit phase
		sort.Slice(readers, func(i, j int) bool {
			a, b := s.stats[readers[i]], s.stats[readers[j]]
			return a.successRatioWeightedMeanLatency() < b.successRatioWeightedMeanLatency()
		})
	}

	for i, maxTake := 0, 1; i < len(readers); maxTake = maxTake * 2 {
		readerSet := make([]METAstate.DataAvailabilityReader, 0, maxTake)
		for taken := 0; taken < maxTake && i < len(readers); i, taken = i+1, taken+1 {
			readerSet = append(readerSet, readers[i])
		}
		readerSets = append(readerSets, readerSet)
	}

	return &basicStrategyInstance{readerSets: readerSets}
}

// Sequential Strategy for Testing
type testingSequentialStrategy struct {
	abstractAggregatorStrategy
}

func (s *testingSequentialStrategy) newInstance() aggregatorStrategyInstance {
	s.RLock()
	defer s.RUnlock()

	si := basicStrategyInstance{}
	for _, reader := range s.readers {
		si.readerSets = append(si.readerSets, []METAstate.DataAvailabilityReader{reader})
	}

	return &si
}

// Instance of a strategy that returns readers in an order according to the strategy
type aggregatorStrategyInstance interface {
	nextReaders() []METAstate.DataAvailabilityReader
}

type basicStrategyInstance struct {
	readerSets [][]METAstate.DataAvailabilityReader
}

func (si *basicStrategyInstance) nextReaders() []METAstate.DataAvailabilityReader {
	if len(si.readerSets) == 0 {
		return nil
	}
	next := si.readerSets[0]
	si.readerSets = si.readerSets[1:]
	return next
}
