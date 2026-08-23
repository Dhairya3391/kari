package util

import (
	"container/list"
	"sync"
)

type cacheEntry[V any] struct {
	key   string
	value V
}

// BoundedCache is a size-capped, thread-safe LRU cache: once more than max
// distinct keys are held, the least-recently-used one (by Get or Set) is
// evicted. It exists for caches that live for the whole process (poster
// images, rendered thumbnails, search results) so a long-running session
// browsing many titles doesn't grow memory without limit, while whatever's
// actually being revisited stays cached rather than getting pushed out by
// simple insertion order.
type BoundedCache[V any] struct {
	mu    sync.Mutex
	max   int
	ll    *list.List
	items map[string]*list.Element
}

// NewBoundedCache creates a cache that holds at most max entries.
func NewBoundedCache[V any](max int) *BoundedCache[V] {
	if max < 1 {
		max = 1
	}
	return &BoundedCache[V]{
		max:   max,
		ll:    list.New(),
		items: make(map[string]*list.Element, max),
	}
}

// Get returns the cached value for key, marking it most-recently-used.
func (c *BoundedCache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*cacheEntry[V]).value, true
	}
	var zero V
	return zero, false
}

// Set inserts or updates key, marking it most-recently-used and evicting
// the least-recently-used entry when the cache exceeds its bound.
func (c *BoundedCache[V]) Set(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		el.Value.(*cacheEntry[V]).value = value
		c.ll.MoveToFront(el)
		return
	}

	el := c.ll.PushFront(&cacheEntry[V]{key: key, value: value})
	c.items[key] = el
	if c.ll.Len() > c.max {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry[V]).key)
		}
	}
}
