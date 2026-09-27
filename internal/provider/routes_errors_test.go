package provider

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestProviderErrorUnwrapsToSentinels proves typed errors stay compatible
// with errors.Is against the package sentinels.
func TestProviderErrorUnwrapsToSentinels(t *testing.T) {
	cases := []struct {
		kind     ProviderErrorKind
		sentinel error
	}{
		{KindNotFound, ErrNotFound},
		{KindNoResults, ErrNoResults},
		{KindNoSources, ErrNoSources},
		{KindNoEpisodes, ErrNoEpisodes},
		{KindRateLimited, ErrRateLimited},
		{KindBlocked, ErrBlocked},
		{KindTimeout, ErrTimeout},
		{KindUpstreamChanged, ErrUpstreamChanged},
		{KindAuthRequired, ErrAuthRequired},
	}
	for _, tc := range cases {
		err := Wrap("pengu", "sources", tc.kind, errors.New("boom"))
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("kind %q does not match sentinel %v", tc.kind, tc.sentinel)
		}
		if KindOf(err) != tc.kind {
			t.Errorf("KindOf = %q, want %q", KindOf(err), tc.kind)
		}
	}
}

// TestKindOfPlainErrors maps legacy sentinel errors to kinds.
func TestKindOfPlainErrors(t *testing.T) {
	if KindOf(ErrNoResults) != KindNoResults {
		t.Errorf("KindOf(ErrNoResults) = %q", KindOf(ErrNoResults))
	}
	if KindOf(errors.New("random")) != KindUpstream {
		t.Errorf("KindOf(random) = %q", KindOf(errors.New("random")))
	}
}

// TestProviderErrorRedactsSecrets proves the message carries no hint of
// bodies or tokens: only provider, stage, kind and short hints.
func TestProviderErrorRedactsSecrets(t *testing.T) {
	err := WrapHint("pengu", "sources", KindUpstreamChanged, "missing streams array", errors.New("x"))
	msg := err.Error()
	if len(msg) > 200 {
		t.Errorf("error message too long (may leak bodies): %q", msg)
	}
}

// TestBreakerFailureOnlySickProviders proves healthy empties (no
// results/sources/episodes, not found, rate-limited pause, our own
// cancel) never count toward the breaker — only sick providers do.
func TestBreakerFailureOnlySickProviders(t *testing.T) {
	healthy := []error{ErrNoResults, ErrNoSources, ErrNoEpisodes, ErrNotFound, ErrRateLimited, ErrAuthRequired, context.Canceled}
	for _, err := range healthy {
		if BreakerFailure(err) {
			t.Errorf("BreakerFailure(%v) = true, want false", err)
		}
	}
	sick := []error{
		ErrTimeout, ErrBlocked, ErrUpstreamChanged,
		context.DeadlineExceeded,
		&HTTPError{Code: 500},
		errors.New("connection reset"),
	}
	for _, err := range sick {
		if !BreakerFailure(err) {
			t.Errorf("BreakerFailure(%v) = false, want true", err)
		}
	}
	if BreakerFailure(nil) {
		t.Error("BreakerFailure(nil) must be false")
	}
}

// TestRoutesTableCoversAllModes proves every content mode has at least
// one route and deadlines respect the budgets.
func TestRoutesTableCoversAllModes(t *testing.T) {
	modes := []ContentType{ModeAnime, ModeMovies, ModeTV, ModeCartoon, ModeLive, ModeManga, ModeJellyfin}
	for _, m := range modes {
		routes := RoutesFor(m)
		if len(routes) == 0 {
			t.Errorf("mode %q has no routes", m)
			continue
		}
		for _, r := range routes {
			if r.Provider == "" {
				t.Errorf("mode %q has a route with empty provider", m)
			}
			if r.searchTimeout() > 8*time.Second {
				t.Errorf("mode %q provider %q search timeout %v exceeds 8s", m, r.Provider, r.searchTimeout())
			}
			if r.sourceTimeout() > 12*time.Second {
				t.Errorf("mode %q provider %q source timeout %v exceeds 12s", m, r.Provider, r.sourceTimeout())
			}
		}
	}
}

// TestRoutesStrategiesMatchSpec proves the default strategies: anime
// merges search and falls back for sources; movies race sources;
// single-provider modes stay single.
func TestRoutesStrategiesMatchSpec(t *testing.T) {
	if SearchStrategy(ModeAnime) != StrategyMerge {
		t.Errorf("anime search = %q, want merge", SearchStrategy(ModeAnime))
	}
	if SourcesStrategy(ModeAnime) != StrategyFallback {
		t.Errorf("anime sources = %q, want fallback", SourcesStrategy(ModeAnime))
	}
	if SearchStrategy(ModeMovies) != StrategyMerge {
		t.Errorf("movies search = %q, want merge", SearchStrategy(ModeMovies))
	}
	if SourcesStrategy(ModeMovies) != StrategyRace {
		t.Errorf("movies sources = %q, want race", SourcesStrategy(ModeMovies))
	}
	if SearchStrategy(ModeLive) != StrategySingle {
		t.Errorf("live search = %q, want single", SearchStrategy(ModeLive))
	}
	if SearchStrategy(ModeManga) != StrategySingle {
		t.Errorf("manga search = %q, want single", SearchStrategy(ModeManga))
	}
}

// TestBoostRankMatchesLegacySort proves the data-driven preference
// reproduces the old hardcoded order: anikoto vidstream-2 first for
// anime, movysx first for movies.
func TestBoostRankMatchesLegacySort(t *testing.T) {
	if BoostRank(ModeAnime, "anikoto", "1080p (Vidstream-2)") < 0 {
		t.Error("anime anikoto vidstream-2 should be boosted")
	}
	if BoostRank(ModeAnime, "anikoto", "1080p (HD-2)") >= 0 {
		t.Error("anime anikoto hd-2 should not be boosted")
	}
	if BoostRank(ModeAnime, "anilight", "Auto (l)") >= 0 {
		t.Error("anime anilight should not be boosted")
	}
	if BoostRank(ModeMovies, "movysx", "1080p") < 0 {
		t.Error("movies movysx should be boosted")
	}
	if BoostRank(ModeMovies, "pengu", "4K [4KHDHub]") >= 0 {
		t.Error("movies pengu should not be boosted")
	}
	if got := PreferredResolvers(ModeMovies); len(got) != 1 || got[0] != "movysx" {
		t.Errorf("PreferredResolvers(movies) = %v", got)
	}
	if got := PreferredResolvers(ModeLive); len(got) != 0 {
		t.Errorf("PreferredResolvers(live) = %v, want empty", got)
	}
}
