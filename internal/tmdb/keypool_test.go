package tmdb

import "testing"

func TestKeyPoolRotatesAndSkipsFailed(t *testing.T) {
	p := NewKeyPool([]string{"a", "b", "", "c"}) // blank ignored

	k1, err := p.NextKey()
	if err != nil || k1 != "a" {
		t.Fatalf("first key: %q %v", k1, err)
	}
	p.MarkFailed("a")

	k2, err := p.NextKey()
	if err != nil || k2 != "b" {
		t.Fatalf("second key: %q %v", k2, err)
	}

	// Wrap-around must skip the failed key.
	_, _ = p.NextKey() // c
	k4, err := p.NextKey()
	if err != nil || k4 != "b" {
		t.Fatalf("rotation after wrap should skip failed a: %q %v", k4, err)
	}
}

func TestKeyPoolAllFailed(t *testing.T) {
	p := NewKeyPool([]string{"only"})
	p.MarkFailed("only")
	if _, err := p.NextKey(); err == nil {
		t.Fatal("exhausted pool should error")
	}
}

func TestKeyPoolEmpty(t *testing.T) {
	p := NewKeyPool(nil)
	if _, err := p.NextKey(); err == nil {
		t.Fatal("empty pool should error")
	}
	var nilPool *KeyPool
	if _, err := nilPool.NextKey(); err == nil {
		t.Fatal("nil pool should error")
	}
	// MarkFailed on nil/blank must not panic.
	nilPool.MarkFailed("")
}
