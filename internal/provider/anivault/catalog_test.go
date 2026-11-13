package anivault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"kari/internal/provider"
)

func newCatalogFixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{
		http:            server.Client(),
		base:            server.URL,
		anilistEndpoint: server.URL + "/graphql",
	}
}

func TestCatalogSearchPreservesCanonicalIDs(t *testing.T) {
	client := newCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"Page":{"media":[{"id":151807,"idMal":52299,"title":{"english":"Solo Leveling"},"format":"TV"}]}}}`)
	})
	results, err := client.Search(context.Background(), "Solo Leveling", provider.ModeAnime)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	result := results[0]
	if result.ID != "151807" || result.AniListID != 151807 || result.MALID != 52299 {
		t.Fatalf("canonical identifiers changed: %+v", result)
	}
}

func TestCatalogResolveTitleUsesMALIdentity(t *testing.T) {
	client := newCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/graphql":
			var request struct {
				Variables struct {
					ID int `json:"id"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Variables.ID != 151807 {
				t.Errorf("AniList lookup ID = %d, want 151807", request.Variables.ID)
			}
			_, _ = fmt.Fprint(w, `{"data":{"Media":{"id":151807,"idMal":52299,"title":{"english":"Solo Leveling"}}}}`)
		case "/api/hoshika/identity-by-legacy/52299":
			_, _ = fmt.Fprint(w, `{"ok":true,"mapped":true,"data":{"shortId":"87654","title":"Solo Leveling"}}`)
		default:
			t.Errorf("unexpected request (ID namespace mix-up): %s", r.URL)
			http.NotFound(w, r)
		}
	})
	shortID, err := client.resolveTitle(context.Background(), "151807")
	if err != nil {
		t.Fatal(err)
	}
	if shortID != "87654" {
		t.Fatalf("Hoshika ID = %q, want 87654", shortID)
	}
}

func TestCatalogResolveTitleExactFallback(t *testing.T) {
	cases := []struct {
		name       string
		identity   string
		status     int
		candidates string
		wantID     string
		wantErr    error
	}{
		{
			name:       "romaji match after unrelated first result",
			identity:   `{"ok":true,"mapped":false}`,
			candidates: `[{"shortId":"111","title":"Another Title"},{"shortId":"87654","title":"Localized Title","titleRomaji":"ORE DAKE LEVEL UP NA KEN!"}]`,
			wantID:     "87654",
		},
		{
			name:       "missing legacy mapping and synonym match",
			status:     http.StatusNotFound,
			identity:   `{"ok":false,"mapped":false}`,
			candidates: `[{"shortId":"87654","titleEnglish":"Only I Level Up"}]`,
			wantID:     "87654",
		},
		{
			name:       "never choose first unrelated result",
			identity:   `{"ok":true,"mapped":false}`,
			candidates: `[{"shortId":"111","title":"Solo Leveling Season 2"}]`,
			wantErr:    provider.ErrNoMatch,
		},
		{
			name:       "reject ambiguous exact matches",
			identity:   `{"ok":true,"mapped":false}`,
			candidates: `[{"shortId":"111","title":"Solo Leveling"},{"shortId":"222","title":"Solo Leveling"}]`,
			wantErr:    provider.ErrNoMatch,
		},
		{
			name:     "reject malformed mapped short id",
			identity: `{"ok":true,"mapped":true,"data":{"shortId":"../151807"}}`,
			wantErr:  provider.ErrUpstreamChanged,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/graphql":
					_, _ = fmt.Fprint(w, `{"data":{"Media":{"id":151807,"idMal":52299,"title":{"english":"Solo Leveling","romaji":"Ore dake Level Up na Ken"},"synonyms":["Only I Level Up"]}}}`)
				case "/api/hoshika/identity-by-legacy/52299":
					if tc.status != 0 {
						w.WriteHeader(tc.status)
					}
					_, _ = fmt.Fprint(w, tc.identity)
				case "/api/hoshika/search":
					if r.URL.Query().Get("limit") != "20" || r.URL.Query().Get("q") == "" {
						t.Errorf("unexpected search parameters: %s", r.URL)
					}
					_, _ = fmt.Fprintf(w, `{"ok":true,"data":%s}`, tc.candidates)
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			})
			got, err := client.resolveTitle(context.Background(), "151807")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("resolve error = %v, want %v", err, tc.wantErr)
			}
			if got != tc.wantID {
				t.Fatalf("Hoshika ID = %q, want %q", got, tc.wantID)
			}
		})
	}
}

func TestCatalogResolveTitleValidatesExplicitIDs(t *testing.T) {
	client := newCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected network request: %s", r.URL)
		http.NotFound(w, r)
	})
	for _, id := range []string{"", "0", "-1", "+21", "hoshika/", "hoshika/0", "hoshika/../21", "hoshika/58090/sub/1"} {
		t.Run(id, func(t *testing.T) {
			if _, err := client.resolveTitle(context.Background(), id); !errors.Is(err, provider.ErrNotFound) {
				t.Fatalf("resolve %q error = %v, want ErrNotFound", id, err)
			}
		})
	}
	if got, err := client.resolveTitle(context.Background(), "hoshika/58090"); err != nil || got != "58090" {
		t.Fatalf("explicit Hoshika ID = %q, %v", got, err)
	}
}

func TestCatalogFetchEpisodesCompleteRoster(t *testing.T) {
	client := newCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/hoshika/anime/58090":
			_, _ = fmt.Fprint(w, `{"ok":true,"data":{"episodes":1181,"title":"One Piece","type":"TV"}}`)
		case "/api/hoshika/anime/58090/episodes":
			entries := make([]catalogEpisode, 0, 51)
			for number := 1; number <= 50; number++ {
				entries = append(entries, catalogEpisode{
					Number: float64(number),
					Title:  "Metadata " + strconv.Itoa(number),
					Filler: number == 50,
				})
			}
			entries = append(entries, catalogEpisode{Number: 50.5, Title: "Fractional special"})
			_ = json.NewEncoder(w).Encode(catalogEpisodesResponse{OK: true, Data: entries})
		default:
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
		}
	})
	series := provider.SearchResult{ID: "hoshika/58090", Title: "One Piece"}
	episodes, err := client.FetchEpisodes(context.Background(), series)
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 1181*2 {
		t.Fatalf("got %d entries, want 2362", len(episodes))
	}
	for index, episode := range episodes {
		number := index/2 + 1
		audio := provider.AudioSub
		if index%2 != 0 {
			audio = provider.AudioDub
		}
		wantID := fmt.Sprintf("watch/anivault/hoshika/58090/%s/%d", audio, number)
		if episode.ID != wantID || episode.Episode != number || episode.Audio != audio || episode.Season != 1 {
			t.Fatalf("entry %d = %+v, want %s", index, episode, wantID)
		}
		wantTitle := "Episode " + strconv.Itoa(number)
		if number <= 50 {
			wantTitle = "Metadata " + strconv.Itoa(number)
		}
		if episode.Title != wantTitle || episode.Filler != (number == 50) {
			t.Fatalf("incorrect metadata at %d: %+v", index, episode)
		}
	}
}

func TestCatalogFetchEpisodesWithoutMetadata(t *testing.T) {
	client := newCatalogFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/hoshika/anime/58090" {
			_, _ = fmt.Fprint(w, `{"ok":true,"data":{"episodes":1181}}`)
			return
		}
		if r.URL.Path != "/api/hoshika/anime/58090/episodes" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		http.NotFound(w, r)
	})
	episodes, err := client.FetchEpisodes(context.Background(), provider.SearchResult{ID: "hoshika/58090"})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 2362 || episodes[2361].Title != "Episode 1181" {
		t.Fatalf("metadata absence truncated roster: %d entries", len(episodes))
	}
}
