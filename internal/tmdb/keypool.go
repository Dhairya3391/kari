package tmdb

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

type KeyPool struct {
	mu     sync.Mutex
	keys   []string
	failed map[string]struct{}
	next   int
}

func NewKeyPool(keys []string) *KeyPool {
	cleaned := make([]string, 0, len(keys))
	for _, key := range keys {
		if k := strings.TrimSpace(key); k != "" {
			cleaned = append(cleaned, k)
		}
	}
	return &KeyPool{keys: cleaned, failed: make(map[string]struct{})}
}

func (p *KeyPool) NextKey() (string, error) {
	return p.nextKey()
}

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
