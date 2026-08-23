package util

import "testing"

func TestBoundedCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := NewBoundedCache[string](2)
	c.Set("a", "1")
	c.Set("b", "2")

	// Touch "a" so "b" becomes the least recently used.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected a to be cached")
	}
	c.Set("c", "3")

	if _, ok := c.Get("b"); ok {
		t.Fatal("expected b (LRU) to be evicted")
	}
	for _, key := range []string{"a", "c"} {
		if v, ok := c.Get(key); !ok || v != map[string]string{"a": "1", "c": "3"}[key] {
			t.Fatalf("key %q missing or wrong: %q %v", key, v, ok)
		}
	}
}

func TestBoundedCacheSetUpdatesExisting(t *testing.T) {
	c := NewBoundedCache[int](4)
	c.Set("k", 1)
	c.Set("k", 2)
	v, ok := c.Get("k")
	if !ok || v != 2 {
		t.Fatalf("update failed: %d %v", v, ok)
	}
}

func TestBoundedCacheMinSizeClamp(t *testing.T) {
	c := NewBoundedCache[string](0)
	c.Set("x", "y")
	c.Set("z", "w")
	// Clamped to capacity 1: only the newest survives.
	if _, ok := c.Get("x"); ok {
		t.Fatal("cache larger than clamped capacity")
	}
	if _, ok := c.Get("z"); !ok {
		t.Fatal("newest entry should survive")
	}
}
