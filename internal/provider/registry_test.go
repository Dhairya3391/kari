package provider

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider is a configurable Provider double used to exercise registry
// aggregation without network access.
type fakeProvider struct {
	name  string
	modes []Mode
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Modes() []Mode {
	return f.modes
}
func (f *fakeProvider) Search(ctx context.Context, query string, mode ContentType) ([]SearchResult, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeProvider) FetchEpisodes(ctx context.Context, series SearchResult) ([]Episode, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeProvider) ResolveSource(ctx context.Context, mediaID string, episode Episode) ([]MediaSource, error) {
	return nil, errors.New("not implemented")
}

func TestRegistryRegisterDeduplicatesByName(t *testing.T) {
	r := &Registry{}
	a := &fakeProvider{name: "dup", modes: []Mode{{Name: ModeMovies, Priority: 1}}}
	b := &fakeProvider{name: "dup", modes: []Mode{{Name: ModeTV, Priority: 1}}}
	r.Register(a)
	r.Register(b)

	got := r.ProvidersForMode(ModeTV)
	if len(got) != 0 {
		t.Fatalf("re-registered name should be ignored, got %d providers for tv", len(got))
	}
	if got = r.ProvidersForMode(ModeMovies); len(got) != 1 {
		t.Fatalf("want 1 provider for movies, got %d", len(got))
	}
}

func TestRegistryProvidersForModeOrdersByPriority(t *testing.T) {
	r := &Registry{}
	r.Register(&fakeProvider{name: "low", modes: []Mode{{Name: ModeAnime, Priority: 5}}})
	r.Register(&fakeProvider{name: "high", modes: []Mode{{Name: ModeAnime, Priority: 1}}})
	r.Register(&fakeProvider{name: "other", modes: []Mode{{Name: ModeMovies, Priority: 1}}})

	got := r.ProvidersForMode(ModeAnime)
	if len(got) != 2 {
		t.Fatalf("want 2 anime providers, got %d", len(got))
	}
	if got[0].Name() != "high" || got[1].Name() != "low" {
		t.Fatalf("priority ordering wrong: %s before %s expected high before low", got[0].Name(), got[1].Name())
	}
}

func TestRegistryFeaturesAggregation(t *testing.T) {
	tests := []struct {
		name string
		reg  func() *Registry
		mode ContentType
		want Features
	}{
		{
			name: "no feature sources defaults to cacheable only",
			reg: func() *Registry {
				r := &Registry{}
				r.Register(&fakeProvider{name: "p", modes: []Mode{{Name: ModeMovies}}})
				return r
			},
			mode: ModeMovies,
			want: Features{},
		},
		{
			name: "any allow-empty wins, all must be cacheable",
			reg: func() *Registry {
				r := &Registry{}
				r.Register(&featureFake{"a", []Mode{{Name: ModeJellyfin}}, Features{AllowEmptyQuery: true, NoCachedSearches: true}})
				r.Register(&featureFake{"b", []Mode{{Name: ModeJellyfin}}, Features{NoCachedSearches: true}})
				return r
			},
			mode: ModeJellyfin,
			want: Features{AllowEmptyQuery: true, NoCachedSearches: true},
		},
		{
			name: "one non-cacheable provider disables caching for the mode",
			reg: func() *Registry {
				r := &Registry{}
				r.Register(&featureFake{"a", []Mode{{Name: ModeMovies}}, Features{}})
				r.Register(&featureFake{"b", []Mode{{Name: ModeMovies}}, Features{NoCachedSearches: true}})
				return r
			},
			mode: ModeMovies,
			want: Features{NoCachedSearches: true},
		},
		{
			name: "first declared placeholder wins",
			reg: func() *Registry {
				r := &Registry{}
				r.Register(&featureFake{"a", []Mode{{Name: ModeTV}}, Features{SearchPlaceholder: "first"}})
				r.Register(&featureFake{"b", []Mode{{Name: ModeTV}}, Features{SearchPlaceholder: "second"}})
				return r
			},
			mode: ModeTV,
			want: Features{SearchPlaceholder: "first"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.reg().Features(tt.mode)
			if got != tt.want {
				t.Fatalf("Features(%s)=%+v want %+v", tt.mode, got, tt.want)
			}
		})
	}
}

type featureFake struct {
	name string
	mode []Mode
	feat Features
}

func (f *featureFake) Name() string { return f.name }
func (f *featureFake) Modes() []Mode {
	return f.mode
}
func (f *featureFake) Features(mode ContentType) Features { return f.feat }
func (f *featureFake) Search(ctx context.Context, query string, mode ContentType) ([]SearchResult, error) {
	return nil, errors.New("not implemented")
}
func (f *featureFake) FetchEpisodes(ctx context.Context, series SearchResult) ([]Episode, error) {
	return nil, errors.New("not implemented")
}
func (f *featureFake) ResolveSource(ctx context.Context, mediaID string, episode Episode) ([]MediaSource, error) {
	return nil, errors.New("not implemented")
}

func TestRegistryAudioLanguagesUnionDedupedByCode(t *testing.T) {
	r := &Registry{}
	r.Register(&langFake{"a", []Mode{{Name: ModeMovies}}, []AudioLanguage{{"en", "English"}, {"hi", "Hindi"}}})
	r.Register(&langFake{"b", []Mode{{Name: ModeMovies}}, []AudioLanguage{{"EN", "English"}, {"ta", "Tamil"}}})
	r.Register(&langFake{"c", []Mode{{Name: ModeAnime}}, []AudioLanguage{{"ja", "Japanese"}}})

	got := r.AudioLanguages(ModeMovies)
	if len(got) != 3 || got[0].Code != "en" || got[2].Code != "ta" {
		t.Fatalf("union wrong: %+v", got)
	}
	if got = r.AudioLanguages(ModeAnime); len(got) != 1 || got[0].Display != "Japanese" {
		t.Fatalf("anime languages wrong: %+v", got)
	}
	if got = r.AudioLanguages(ModeCartoon); len(got) != 0 {
		t.Fatalf("mode without declarations should be empty, got %+v", got)
	}
}

type langFake struct {
	name  string
	mode  []Mode
	langs []AudioLanguage
}

func (f *langFake) Name() string { return f.name }
func (f *langFake) Modes() []Mode {
	return f.mode
}
func (f *langFake) AudioLanguages() []AudioLanguage { return f.langs }
func (f *langFake) Search(ctx context.Context, q string, m ContentType) ([]SearchResult, error) {
	return nil, errors.New("not implemented")
}
func (f *langFake) FetchEpisodes(ctx context.Context, s SearchResult) ([]Episode, error) {
	return nil, errors.New("not implemented")
}
func (f *langFake) ResolveSource(ctx context.Context, id string, e Episode) ([]MediaSource, error) {
	return nil, errors.New("not implemented")
}

// movieFlowFake implements MovieEpisodeFlow.
type movieFlowFake struct {
	fakeProvider
	requires bool
}

func (f *movieFlowFake) RequiresEpisodeListForMovies() bool { return f.requires }

func TestRegistryRequiresEpisodeListForMovies(t *testing.T) {
	r := &Registry{}
	r.Register(&movieFlowFake{fakeProvider{name: "needs", modes: []Mode{{Name: ModeAnime}}}, true})
	r.Register(&movieFlowFake{fakeProvider{name: "direct", modes: []Mode{{Name: ModeMovies}}}, false})
	r.Register(&fakeProvider{name: "plain", modes: []Mode{{Name: ModeCartoon}}})

	for name, want := range map[string]bool{
		"needs":   true,
		"direct":  false,
		"plain":   false, // no capability => direct resolve default
		"unknown": false, // unknown provider => direct resolve default
	} {
		if got := r.RequiresEpisodeListForMovies(name); got != want {
			t.Errorf("RequiresEpisodeListForMovies(%q)=%v want %v", name, got, want)
		}
	}
}

// aliasFake implements Presenter.
type aliasFake struct {
	fakeProvider
	alias string
}

func (f *aliasFake) Alias() string { return f.alias }

func TestRegistryDisplayNameFallsBackToName(t *testing.T) {
	r := &Registry{}
	r.Register(&aliasFake{fakeProvider{name: "internal1"}, "Shinchan"})
	r.Register(&fakeProvider{name: "internal2", modes: []Mode{{Name: ModeMovies}}})

	if got := r.DisplayName("internal1"); got != "Shinchan" {
		t.Errorf("alias not used: %q", got)
	}
	if got := r.DisplayName("internal2"); got != "internal2" {
		t.Errorf("missing alias should fall back to name: %q", got)
	}
	if got := r.DisplayName("ghost"); got != "ghost" {
		t.Errorf("unknown provider should echo input: %q", got)
	}
}

func TestAllModesSortedAndUnique(t *testing.T) {
	r := &Registry{}
	r.Register(&fakeProvider{name: "a", modes: []Mode{{Name: ModeTV}, {Name: ModeAnime}}})
	r.Register(&fakeProvider{name: "b", modes: []Mode{{Name: ModeAnime}, {Name: ModeMovies}}})

	got := r.AllModes()
	want := []ContentType{ModeAnime, ModeMovies, ModeTV}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("modes not sorted unique: %v", got)
		}
	}
}
