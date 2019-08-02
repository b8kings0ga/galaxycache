/*
Copyright 2012 Vimeo Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package galaxycache

import (
	"math"
	"sync"
	"time"

	"github.com/vimeo/galaxycache/promoter"
)

// Promoter is the interface for determining whether a key/value pair should be
// added to the hot cache
type Promoter interface {
	ShouldPromote(key string, data []byte, stats promoter.Stats) bool
}

// PromoterFunc implements Promoter with a function.
type PromoterFunc func(key string, data []byte, stats promoter.Stats) bool

// ShouldPromote returns true if the given key/data pair has been chosen to
// add to the hotcache
func (f PromoterFunc) ShouldPromote(key string, data []byte, stats promoter.Stats) bool {
	return f(key, data, stats)
}

func (g *Galaxy) updateHotCacheStats() {
	if g.hotCache.lru == nil {
		g.hotCache.initLRU()
	}
	mruEleQPS := 0.0
	lruEleQPS := 0.0
	mruEle := g.hotCache.lru.MostRecent()
	lruEle := g.hotCache.lru.LeastRecent()
	if mruEle != nil { // lru contains at least one element
		mruEleQPS = mruEle.(*valWithStat).stats.Val()
		lruEleQPS = lruEle.(*valWithStat).stats.Val()
	}

	newHCS := &promoter.HCStats{
		MostRecentQPS:  mruEleQPS,
		LeastRecentQPS: lruEleQPS,
		HCSize:         (g.cacheBytes / g.hcRatio) - g.hotCache.bytes(),
		HCCapacity:     g.cacheBytes / g.hcRatio,
	}
	g.hcStats = newHCS
}

// keyStats keeps track of the hotness of a key
type keyStats struct {
	dQPS *dampedQPS
}

func newValWithStat(data []byte, kStats *keyStats) *valWithStat {
	value := &valWithStat{
		data: data,
		stats: &keyStats{
			dQPS: &dampedQPS{
				period: time.Second,
			},
		},
	}
	if kStats != nil {
		value.stats = kStats
	}
	return value
}

func (k *keyStats) Val() float64 {
	return k.dQPS.val(time.Now())
}

func (k *keyStats) Touch() {
	k.dQPS.touch(time.Now())
}

// dampedQPS is an average that recombines the current state with the previous.
type dampedQPS struct {
	mu     sync.Mutex
	period time.Duration
	t      time.Time
	prev   float64
	ct     float64
}

// must be between 0 and 1, the fraction of the new value that comes from
// current rather than previous.
// if `samples` is the number of samples into the damped weighted average you
// want to maximize the fraction of the contribution after; x is the damping
// constant complement (this way we don't have to multiply out (1-x) ^ samples)
// f(x) = (1 - x) * x ^ samples = x ^samples - x ^(samples + 1)
// f'(x) = samples * x ^ (samples - 1) - (samples + 1) * x ^ samples
// this yields a critical point at x = (samples - 1) / samples
const dampingConstant = (1.0 / 30.0) // 5 minutes (30 samples at a 10s interval)
const dampingConstantComplement = 1.0 - dampingConstant

func (a *dampedQPS) touch(now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.t.IsZero() {
		a.ct++
		a.t = now
		return
	}
	a.maybeFlush(now)
	a.ct++
}

func (a *dampedQPS) maybeFlush(now time.Time) {
	if now.Sub(a.t) >= a.period {
		prev, cur := a.prev, a.ct
		exponent := math.Floor(float64(now.Sub(a.t))/float64(a.period)) - 1
		a.prev = ((dampingConstant * cur) + (dampingConstantComplement * prev)) * math.Pow(dampingConstantComplement, exponent)
		a.ct = 0
		a.t = now
	}
}

func (a *dampedQPS) val(now time.Time) float64 {
	a.mu.Lock()
	if a.t.IsZero() {
		a.t = now
	}
	a.maybeFlush(now)
	prev := a.prev
	a.mu.Unlock()
	return prev
}

func (g *Galaxy) addNewToCandidateCache(key string) *keyStats {
	kStats := &keyStats{
		dQPS: &dampedQPS{
			period: time.Second,
		},
	}

	g.candidateCache.addToCandidateCache(key, kStats)
	return kStats
}

func (c *cache) addToCandidateCache(key string, kStats *keyStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.Add(key, kStats)
}
