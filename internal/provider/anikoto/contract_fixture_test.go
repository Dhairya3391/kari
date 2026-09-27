package anikoto

import (
	"context"
	"testing"

	"kari/internal/provider"
	"kari/internal/provider/providertest"
)

// TestContractAgainstFixtures runs the shared contract suite against the
// direct chain: AniList GraphQL plus the scraped Anikoto site, with no
// intermediary API.
func TestContractAgainstFixtures(t *testing.T) {
	fx := newDirectFixture(t, "21", "One Piece", "one-piece", "789")
	client := fx.client(t)

	eps, err := client.FetchEpisodes(context.Background(), provider.SearchResult{ID: "21"})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) == 0 {
		t.Fatal("need at least one episode for the contract run")
	}

	series := provider.SearchResult{Title: "One Piece", ID: "21", Type: provider.ModeAnime, MediaType: provider.MediaTypeAnime}
	providertest.Run(t, providertest.Spec{
		Mode:    provider.ModeAnime,
		Kinds:   []provider.ContentType{provider.ModeAnime},
		Series:  series,
		Episode: eps[0],
		MediaID: "21",
		Search: func(ctx context.Context) ([]provider.SearchResult, error) {
			return client.Search(ctx, "one piece", provider.ModeAnime)
		},
		Items: func(ctx context.Context, s provider.SearchResult) ([]provider.Episode, error) {
			return client.FetchEpisodes(ctx, s)
		},
		Sources: func(ctx context.Context, mediaID string, e provider.Episode) ([]provider.MediaSource, error) {
			return client.ResolveSource(ctx, mediaID, e)
		},
	})
}
