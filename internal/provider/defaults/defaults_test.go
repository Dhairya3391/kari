package defaults

import (
	"testing"

	"kari/internal/config"
	"kari/internal/tmdb"
)

func TestDefaultRegistryIncludesNewProviders(t *testing.T) {
	keyPool := &tmdb.KeyPool{}
	cfg := &config.Config{}

	reg, err := NewDefaultRegistry(keyPool, cfg)
	if err != nil {
		t.Fatalf("NewDefaultRegistry: %v", err)
	}
	for _, name := range []string{"cno", "cinecat", "movysx", "a11", "vyla", "atsu", "famelack", "anidream", "pengu"} {
		p, ok := reg.ProviderByName(name)
		if !ok || p == nil {
			t.Errorf("expected provider %q in registry", name)
		}
	}
	if _, ok := reg.ProviderByName("ocecomic"); ok {
		t.Fatal("comic-only provider must not be registered")
	}
}

func TestProviderIDRenames(t *testing.T) {
	renames := ProviderIDRenames()
	// Derived from descriptor declarations, so adding a LegacyIDs entry is
	// enough to migrate a renamed provider's persisted preferences.
	if got := renames["sweetfox"]; got != "vidgold" {
		t.Errorf("sweetfox -> %q, want vidgold", got)
	}
	if got := renames["vidgold"]; got != "" {
		t.Errorf("current ID must not map to itself, got %q", got)
	}
}
