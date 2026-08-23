package tmdb

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// KeyPool rotates through TMDB API keys round-robin, skipping keys marked
// failed after auth errors. It is safe for concurrent use; a nil *KeyPool
// behaves like an empty pool, so components that work without TMDB don't
// need nil-guards at call sites.
type KeyPool struct {
	mu     sync.Mutex
	keys   []string
	failed map[string]struct{}
	next   int
}

// NewKeyPool constructs a pool from the given keys, ignoring blanks.
func NewKeyPool(keys []string) *KeyPool {
	cleaned := make([]string, 0, len(keys))
	for _, key := range keys {
		if k := strings.TrimSpace(key); k != "" {
			cleaned = append(cleaned, k)
		}
	}
	return &KeyPool{keys: cleaned, failed: make(map[string]struct{})}
}

// NextKey returns the next working key, rotating through the pool and
// skipping failed ones. Callers should MarkFailed on auth errors and retry.
func (p *KeyPool) NextKey() (string, error) {
	return p.nextKey()
}

// MarkFailed records that a key was rejected, excluding it from rotation.
// Safe to call with empty keys or on a nil pool.
func (p *KeyPool) MarkFailed(key string) {
	key = strings.TrimSpace(key)
	if key == "" || p == nil {
		return
	}
	p.mu.Lock()
	if p.failed == nil {
		p.failed = make(map[string]struct{})
	}
	p.failed[key] = struct{}{}
	p.mu.Unlock()
}

// nextKey is the lock-holding implementation of NextKey.
func (p *KeyPool) nextKey() (string, error) {
	if p == nil {
		return "", errors.New("tmdb key pool is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.keys) == 0 {
		return "", errors.New("no tmdb keys available")
	}

	start := p.next
	for i := 0; i < len(p.keys); i++ {
		idx := (start + i) % len(p.keys)
		key := p.keys[idx]
		if _, failed := p.failed[key]; failed {
			continue
		}
		p.next = (idx + 1) % len(p.keys)
		return key, nil
	}

	return "", fmt.Errorf("all tmdb keys failed")
}
