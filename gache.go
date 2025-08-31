package cache

import (
	"sync"
	"time"
)

// cache is a simple in-memory key/value store with optional expiration.
// it cleans up expired items in the background.
type Cache[K comparable, V any] struct {
	items sync.Map
	stop  chan struct{}
	once  sync.Once
}

// each stored item has the actual value and an optional expiration time.
type item[V any] struct {
	data    V
	expires time.Time
}

// new creates a cache and starts a background goroutine
// that periodically removes expired items.
func New[K comparable, V any](cleaningInterval time.Duration) *Cache[K, V] {
	c := &Cache[K, V]{
		stop: make(chan struct{}),
	}

	go func() {
		ticker := time.NewTicker(cleaningInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				now := time.Now()
				c.items.Range(func(key, value any) bool {
					it := value.(item[V])
					if !it.expires.IsZero() && now.After(it.expires) {
						c.items.Delete(key)
					}
					return true
				})

			case <-c.stop:
				return
			}
		}
	}()

	return c
}

// get returns the value for the given key.
// if the item is expired or not found, it returns false.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	val, ok := c.items.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	it := val.(item[V])
	if !it.expires.IsZero() && time.Now().After(it.expires) {
		var zero V
		return zero, false
	}
	return it.data, true
}

// set stores a value for the given key.
// if duration <= 0, the value never expires.
func (c *Cache[K, V]) Set(key K, value V, duration time.Duration) {
	var expires time.Time
	if duration > 0 {
		expires = time.Now().Add(duration)
	}
	c.items.Store(key, item[V]{data: value, expires: expires})
}

// range goes through all items and calls f for each non-expired one.
// returning false from f stops the loop early.
func (c *Cache[K, V]) Range(f func(key K, value V) bool) {
	now := time.Now()
	c.items.Range(func(k, v any) bool {
		it := v.(item[V])
		if !it.expires.IsZero() && now.After(it.expires) {
			return true // skip expired
		}
		return f(k.(K), it.data)
	})
}

// delete removes a key from the cache.
func (c *Cache[K, V]) Delete(key K) {
	c.items.Delete(key)
}

// close stops the cleanup goroutine.
// safe to call multiple times.
func (c *Cache[K, V]) Close() {
	c.once.Do(func() {
		close(c.stop)
	})
}
