package defaults

import (
	"testing"

	"kari/internal/config"
	"kari/internal/tmdb"
)

func TestDefaultRegistryIncludesNewProviders(t *testing.T) {
	keyPool := &tmdb.KeyPool{}
	cfg := &config.Config{
		PenguAuthToken: "dummy",
	}

	reg, err := NewDefaultRegistry(keyPool, cfg, nil)
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	for _, name := range []string{"cno", "mapple", "cinecat", "movysx", "a11", "vyla", "atsu", "stability", "timstreams", "famelack", "iptvorg", "anidream"} {
		p, ok := reg.ProviderByName(name)
		if !ok || p == nil {
			t.Errorf("expected provider %q in registry", name)
		}
	}
	if _, ok := reg.ProviderByName("ocecomic"); ok {
		t.Fatal("comic-only provider must not be registered")
	}
}
