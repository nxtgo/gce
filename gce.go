package gce

import (
	"container/list"
	"context"
	"fmt"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"
)

type Cache[K comparable, V any] struct {
	shards      []*shard[K, V]
	shardMask   uint64
	hashSeed    maphash.Seed
	defaultTTL  time.Duration
	cleanupFreq time.Duration
	maxEntries  uint64
	perShardCap uint64
	ctx         context.Context
	cancel      context.CancelFunc
	closed      atomic.Bool
	stats       Stats
	wg          sync.WaitGroup
}

type Stats struct {
	Hits, Misses, Loads, Evictions, CurrentSize uint64
}

type entry[V any] struct {
	value     V
	expiresAt int64
	lruElem   *list.Element
}

type shard[K comparable, V any] struct {
	mu   sync.RWMutex
	data map[K]*entry[V]
	lru  *list.List
	cap  uint64
}

type Option func(*options)
type options struct {
	shardCount  int
	defaultTTL  time.Duration
	cleanupFreq time.Duration
	maxEntries  uint64
}

func WithShardCount(n int) Option { return func(o *options) { o.shardCount = n } }
func WithDefaultTTL(d time.Duration) Option {
	return func(o *options) { o.defaultTTL = d }
}
func WithCleanupInterval(d time.Duration) Option {
	return func(o *options) { o.cleanupFreq = d }
}
func WithMaxEntries(max uint64) Option {
	return func(o *options) { o.maxEntries = max }
}

func New[K comparable, V any](opts ...Option) *Cache[K, V] {
	o := &options{shardCount: 32, cleanupFreq: time.Minute}
	for _, opt := range opts {
		opt(o)
	}
	if o.shardCount <= 0 {
		o.shardCount = 32
	}
	if o.cleanupFreq <= 0 {
		o.cleanupFreq = time.Minute
	}
	shards := nextPow2(o.shardCount)
	c := &Cache[K, V]{
		shards:      make([]*shard[K, V], shards),
		shardMask:   uint64(shards - 1),
		hashSeed:    maphash.MakeSeed(),
		defaultTTL:  o.defaultTTL,
		cleanupFreq: o.cleanupFreq,
		maxEntries:  o.maxEntries,
	}
	if o.maxEntries > 0 {
		c.perShardCap = (o.maxEntries + uint64(shards-1)) / uint64(shards)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.ctx, c.cancel = ctx, cancel

	for i := range shards {
		s := &shard[K, V]{data: make(map[K]*entry[V], 32)}
		if c.perShardCap > 0 {
			s.cap, s.lru = c.perShardCap, list.New()
		}
		c.shards[i] = s
	}

	if c.defaultTTL > 0 || c.maxEntries > 0 {
		c.wg.Go(c.janitor)
	}

	return c
}

func (c *Cache[K, V]) Get(key K) (V, bool) {
	var zero V
	if c.closed.Load() {
		return zero, false
	}
	sh := c.shardFor(key)
	now := time.Now().UnixNano()

	sh.mu.RLock()
	e, ok := sh.data[key]
	if !ok {
		sh.mu.RUnlock()
		atomic.AddUint64(&c.stats.Misses, 1)
		return zero, false
	}
	if e.expiresAt > 0 && now >= e.expiresAt {
		sh.mu.RUnlock()
		c.Delete(key)
		atomic.AddUint64(&c.stats.Misses, 1)
		return zero, false
	}
	val := e.value
	if sh.lru != nil && e.lruElem != nil {
		sh.mu.RUnlock()
		sh.mu.Lock()
		if e2, ok2 := sh.data[key]; ok2 && e2.lruElem != nil {
			sh.lru.MoveToFront(e2.lruElem)
		}
		sh.mu.Unlock()
	} else {
		sh.mu.RUnlock()
	}
	atomic.AddUint64(&c.stats.Hits, 1)
	return val, true
}

func (c *Cache[K, V]) Set(key K, val V, ttl time.Duration) {
	if c.closed.Load() {
		return
	}
	sh := c.shardFor(key)

	var exp int64
	if ttl == 0 {
		ttl = c.defaultTTL
	}
	if ttl > 0 {
		exp = time.Now().Add(ttl).UnixNano()
	}

	sh.mu.Lock()
	defer sh.mu.Unlock()
	if old, ok := sh.data[key]; ok {
		old.value, old.expiresAt = val, exp
		if sh.lru != nil && old.lruElem != nil {
			sh.lru.MoveToFront(old.lruElem)
		}
		return
	}
	e := &entry[V]{value: val, expiresAt: exp}
	sh.data[key] = e
	atomic.AddUint64(&c.stats.Loads, 1)
	atomic.AddUint64(&c.stats.CurrentSize, 1)
	if sh.lru != nil {
		e.lruElem = sh.lru.PushFront(key)
		if sh.cap > 0 && uint64(len(sh.data)) > sh.cap {
			c.evictOne(sh)
		}
	}
}

func (c *Cache[K, V]) Delete(key K) {
	if c.closed.Load() {
		return
	}
	sh := c.shardFor(key)
	sh.mu.Lock()
	if e, ok := sh.data[key]; ok {
		if sh.lru != nil && e.lruElem != nil {
			sh.lru.Remove(e.lruElem)
		}
		delete(sh.data, key)
		atomic.AddUint64(&c.stats.CurrentSize, ^uint64(0))
	}
	sh.mu.Unlock()
}

func (c *Cache[K, V]) Len() uint64 {
	var n uint64
	for _, sh := range c.shards {
		sh.mu.RLock()
		n += uint64(len(sh.data))
		sh.mu.RUnlock()
	}
	return n
}

func (c *Cache[K, V]) Stats() Stats {
	return Stats{
		Hits:        atomic.LoadUint64(&c.stats.Hits),
		Misses:      atomic.LoadUint64(&c.stats.Misses),
		Loads:       atomic.LoadUint64(&c.stats.Loads),
		Evictions:   atomic.LoadUint64(&c.stats.Evictions),
		CurrentSize: atomic.LoadUint64(&c.stats.CurrentSize),
	}
}

func (c *Cache[K, V]) Purge() {
	for _, sh := range c.shards {
		sh.mu.Lock()
		sh.data = make(map[K]*entry[V])
		if sh.lru != nil {
			sh.lru.Init()
		}
		sh.mu.Unlock()
	}
	atomic.StoreUint64(&c.stats.CurrentSize, 0)
}

func (c *Cache[K, V]) Close() {
	if c.closed.CompareAndSwap(false, true) {
		c.cancel()
		c.wg.Wait()
	}
}

func (c *Cache[K, V]) shardFor(key K) *shard[K, V] {
	var h maphash.Hash
	h.SetSeed(c.hashSeed)
	_, _ = h.Write([]byte(anyToBytes(key)))
	return c.shards[h.Sum64()&c.shardMask]
}

func anyToBytes[K comparable](k K) []byte {
	switch v := any(k).(type) {
	case string:
		return []byte(v)
	case []byte:
		return v
	case int:
		return []byte(string(rune(v)))
	default:
		return fmt.Appendf([]byte{}, "%v", v)
	}
}

func (c *Cache[K, V]) evictOne(sh *shard[K, V]) {
	if sh.lru == nil || sh.lru.Len() == 0 {
		return
	}
	el := sh.lru.Back()
	if el == nil {
		return
	}
	key := el.Value.(K)
	if e, ok := sh.data[key]; ok {
		delete(sh.data, key)
		atomic.AddUint64(&c.stats.CurrentSize, ^uint64(0))
		atomic.AddUint64(&c.stats.Evictions, 1)
		if e.lruElem != nil {
			sh.lru.Remove(e.lruElem)
		}
	}
}

func (c *Cache[K, V]) janitor() {
	t := time.NewTicker(c.cleanupFreq)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			now := time.Now().UnixNano()
			for _, sh := range c.shards {
				sh.mu.Lock()
				for k, e := range sh.data {
					if e.expiresAt > 0 && now >= e.expiresAt {
						if sh.lru != nil && e.lruElem != nil {
							sh.lru.Remove(e.lruElem)
						}
						delete(sh.data, k)
						atomic.AddUint64(&c.stats.Evictions, 1)
						atomic.AddUint64(&c.stats.CurrentSize, ^uint64(0))
					}
				}
				sh.mu.Unlock()
			}
		}
	}
}

func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	for i := 1; i < 64; i <<= 1 {
		n |= n >> i
	}
	return n + 1
}
