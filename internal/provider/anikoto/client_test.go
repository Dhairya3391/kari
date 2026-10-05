package anikoto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kari/internal/provider"
)

// directFixture serves the full direct chain: AniList GraphQL at /,
// the Anikoto site search, watch page, episode list, server list,
// server embed lookup, embed page, and Megaplay getSources.
type directFixture struct {
	server     *httptest.Server
	anilistID  string
	animeTitle string
	slug       string
	animeID    string
	dataIDs    string
	encToken   string
}

// Canned Megaplay enc token (same keys as production); decrypts to a
// nexabloom master.m3u8. Copied from the crypto test vector.
const fixtureEnc = "wdeBruh3qqn_i5wUNnyaPcXqidp1UWP84FfPHzGyKXDMoY_RzXqbC0h49XmoI7d0vYZArA5rcKY-FQxnEk8NNEWezec9dd2jwxp1UbLN43p_7CMxXPDF5BUX86bUm0_Uuw0-dlv_yT9MKsnmlOgDFm3ReCPZPJloNlqBF7aftTk"

func newDirectFixture(t *testing.T, anilistID, title, slug, animeID string) *directFixture {
	t.Helper()
	fx := &directFixture{
		anilistID:  anilistID,
		animeTitle: title,
		slug:       slug,
		animeID:    animeID,
		dataIDs:    "srv1,srv2",
		encToken:   fixtureEnc,
	}
	mux := http.NewServeMux()
	var base string

	// AniList GraphQL (search + media) at the server root.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "Media(") {
			fmt.Fprintf(w, `{"data":{"Media":{"id":%s,"idMal":52299,"title":{"romaji":%q,"english":%q,"userPreferred":%q,"native":"X"},"synonyms":["SL"],"format":"TV","status":"FINISHED","seasonYear":2024,"episodes":12,"nextAiringEpisode":null}}}`,
				anilistID, title, title, title)
			return
		}
		fmt.Fprintf(w, `{"data":{"Page":{"media":[
			{"id":%s,"title":{"romaji":%q,"english":%q,"userPreferred":%q,"native":"X"},"seasonYear":2024,"format":"TV"},
			{"id":184694,"title":{"romaji":"Movie Romaji","english":"","userPreferred":"","native":""},"seasonYear":2024,"format":"MOVIE"}
		]}}}`, anilistID, title, title, title)
	})

	// Anikoto site search page (poster blocks with data-tip IDs).
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="ani poster tip" data-tip=%q><a href="%s/watch/%s/ep-1"><img alt=%q></a></div>`,
			animeID, base, slug, title)
	})

	// Anikoto watch page carries the internal anime ID on the player node.
	mux.HandleFunc("/watch/"+slug, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><div id="watch-main" data-id=%q></div></body></html>`, animeID)
	})

	// Episode list AJAX endpoint.
	mux.HandleFunc("/ajax/episode/list/"+animeID, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"result":%q}`,
			`<a href="#" data-id="e1" data-num="1" data-slug="1" data-sub="1" data-dub="1" data-ids="`+fx.dataIDs+`"><b>1</b><span>Ep One</span></a>`+
				`<a href="#" data-id="e2" data-num="2" data-slug="2" data-sub="1" data-dub="0" data-ids="`+fx.dataIDs+`"><b>2</b><span>Ep Two</span></a>`+
				`<a href="#" data-id="e3" data-num="0.5" data-slug="x" data-sub="1" data-dub="0" data-ids="`+fx.dataIDs+`"><b>0.5</b><span>Recap</span></a>`)
	})

	// Server list AJAX endpoint (label + list markup like the live site).
	mux.HandleFunc("/ajax/server/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"result":%q}`,
			`<div class="servers"><div class="type" data-type="sub"><label><i></i> SUB</label><ul><li data-ep-id="e1" data-sv-id="1" data-link-id="abc123">Vidstream-2</li></ul></div></div>`)
	})

	// Server embed lookup.
	mux.HandleFunc("/ajax/server", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("get") != "abc123" {
			http.Error(w, "unknown server", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"result":{"url":%q}}`, "http://"+r.Host+"/embed/999")
	})

	// Embed page carries the file ID.
	mux.HandleFunc("/embed/999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><div class="player" data-id="999">File 999 - sub</div></body></html>`)
	})

	// Megaplay getSources returns the encrypted token plus tracks.
	mux.HandleFunc("/stream/getSources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "999" {
			http.Error(w, "unknown file", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"enc":%q,"tracks":[{"file":"https://cdn.example.com/sub.vtt","label":"English","kind":"captions"}]}`,
			fx.encToken)
	})

	fx.server = httptest.NewServer(mux)
	base = fx.server.URL
	t.Cleanup(fx.server.Close)
	return fx
}

func (fx *directFixture) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClientWithBaseURL(fx.server.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL: %v", err)
	}
	return c
}

func TestAnikotoSearch(t *testing.T) {
	fx := newDirectFixture(t, "151807", "Solo Leveling", "solo-leveling", "456")
	c := fx.client(t)

	results, err := c.Search(context.Background(), "Solo Leveling", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "151807" || results[0].Title != "Solo Leveling" {
		t.Errorf("unexpected results[0]: %+v", results[0])
	}
	if results[1].MediaType != provider.MediaTypeMovie {
		t.Errorf("movie format must map to MediaTypeMovie: %+v", results[1])
	}
	if results[0].MediaType != provider.MediaTypeAnime || results[0].Year != "2024" {
		t.Errorf("unexpected results[0] meta: %+v", results[0])
	}
}

func TestAnikotoSearchEmptyQuery(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Search(context.Background(), "", provider.ModeAnime); err == nil {
		t.Error("empty query must error")
	}
}

// TestAnikotoCapabilities pins the static contract surface.
func TestAnikotoCapabilities(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Alias() == "" || c.Name() != "anikoto" {
		t.Errorf("identity wrong: %q %q", c.Alias(), c.Name())
	}
	if len(c.Modes()) != 1 || c.Modes()[0].Name != provider.ModeAnime {
		t.Errorf("modes = %+v", c.Modes())
	}
	if !c.RequiresEpisodeListForMovies() {
		t.Error("movies must go through the episode flow")
	}
	if !c.Features(provider.ModeAnime).AudioSelection {
		t.Error("anime must declare audio selection")
	}
	if got := c.Features(provider.ModeMovies); got.AudioSelection {
		t.Errorf("non-anime features = %+v", got)
	}
}

func TestAnikotoFetchEpisodes(t *testing.T) {
	fx := newDirectFixture(t, "151808", "Solo Leveling", "solo-leveling", "456")
	c := fx.client(t)

	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "151808"})
	if err != nil {
		t.Fatalf("FetchEpisodes failed: %v", err)
	}

	// Ep 1 sub+dub, ep 2 sub-only; fractional 0.5 skipped = 3.
	if len(eps) != 3 {
		t.Fatalf("expected 3 episodes, got %d", len(eps))
	}
	if eps[0].Episode != 1 || eps[0].Audio != "sub" {
		t.Errorf("unexpected eps[0]: %+v", eps[0])
	}
	if eps[0].Title != "Ep One" {
		t.Errorf("episode title must come from anchor text, got %q", eps[0].Title)
	}
	if !strings.Contains(eps[0].ID, "|srv1,srv2") {
		t.Errorf("episode ID must embed data-ids: %q", eps[0].ID)
	}
	if eps[1].Episode != 1 || eps[1].Audio != "dub" {
		t.Errorf("unexpected eps[1]: %+v", eps[1])
	}
	if eps[2].Episode != 2 || eps[2].Audio != "sub" {
		t.Errorf("unexpected eps[2]: %+v", eps[2])
	}
}

func TestAnikotoResolveSource(t *testing.T) {
	fx := newDirectFixture(t, "151809", "Solo Leveling", "solo-leveling", "456")
	c := fx.client(t)

	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "151809"})
	if err != nil {
		t.Fatalf("FetchEpisodes failed: %v", err)
	}

	sources, err := c.ResolveSource(context.Background(), "151809", eps[0])
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	s1 := sources[0]
	if !strings.HasPrefix(s1.URL, "https://fetch.nexabloom.top/") || !strings.Contains(s1.URL, "?token=") {
		t.Errorf("expected signed direct CDN URL, got %s", s1.URL)
	}
	if strings.Contains(s1.URL, "127.0.0.1") || strings.Contains(s1.URL, "localhost") || strings.Contains(s1.URL, "/proxy/") {
		t.Errorf("source must never be a proxy URL: %s", s1.URL)
	}
	if !strings.Contains(s1.Quality, "Vidstream-2") {
		t.Errorf("expected Vidstream-2 quality tag, got %s", s1.Quality)
	}
	if s1.Type != provider.SourceTypeHLS {
		t.Errorf("expected hls type, got %s", s1.Type)
	}
	if s1.Referer == "" || s1.UserAgent == "" {
		t.Errorf("mpv needs referer + user-agent: %+v", s1)
	}
	if len(s1.Subtitles) != 1 || s1.Subtitles[0].URL != "https://cdn.example.com/sub.vtt" {
		t.Errorf("unexpected subtitles: %+v", s1.Subtitles)
	}

	// The fixture server list is sub-only: a dub request must fail loudly
	// with the audio-unavailable sentinel, never silently play sub audio.
	_, err = c.ResolveSource(context.Background(), "151809", eps[1])
	if !errors.Is(err, provider.ErrAudioUnavailable) {
		t.Errorf("dub resolve err = %v, want ErrAudioUnavailable", err)
	}
}

func TestAnikotoResolveSourceInvalid(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.ResolveSource(context.Background(), "151807", provider.Episode{}); err == nil {
		t.Error("empty episode must error")
	}
}

func TestBlackCloverSeason2Resolution(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	animeID, slug, err := c.resolveAnilistToAnikoto(context.Background(), "195604")
	if err != nil {
		t.Fatalf("resolveAnilistToAnikoto(195604) failed: %v", err)
	}
	if animeID != "8839" {
		t.Errorf("animeID = %q, want 8839", animeID)
	}
	if slug != "black-clover-season-2" {
		t.Errorf("slug = %q, want black-clover-season-2", slug)
	}
}

func TestSlugCandidates(t *testing.T) {
	candidates := slugCandidates([]string{"Black Clover Season 2", "Black Clover 2nd Season"})
	foundSeason2 := false
	found2ndSeason := false
	for _, s := range candidates {
		if s == "black-clover-season-2" {
			foundSeason2 = true
		}
		if s == "black-clover-2nd-season" {
			found2ndSeason = true
		}
	}
	if !foundSeason2 || !found2ndSeason {
		t.Errorf("slugCandidates missing expected slugs: %v", candidates)
	}
}

func TestScoreCandidateSeasonSeparation(t *testing.T) {
	targetS2 := []string{"Black Clover Season 2", "Black Clover 2nd Season"}
	targetS1 := []string{"Black Clover"}

	// Season 2 target must match Season 2 candidate with 1000 (exact)
	if score := scoreCandidate("Black Clover Season 2", targetS2); score != 1000 {
		t.Errorf("score for 'Black Clover Season 2' against S2 = %d, want 1000", score)
	}
	if score := scoreCandidate("Black Clover 2nd Season", targetS2); score != 1000 {
		t.Errorf("score for 'Black Clover 2nd Season' against S2 = %d, want 1000", score)
	}

	// Season 2 target MUST NOT match Season 1 candidate
	if score := scoreCandidate("Black Clover", targetS2); score != 0 {
		t.Errorf("score for 'Black Clover' against S2 = %d, want 0", score)
	}

	// Season 1 target MUST match Season 1 candidate with 1000 (exact)
	if score := scoreCandidate("Black Clover", targetS1); score != 1000 {
		t.Errorf("score for 'Black Clover' against S1 = %d, want 1000", score)
	}

	// Season 1 target MUST NOT match Season 2 candidate
	if score := scoreCandidate("Black Clover Season 2", targetS1); score != 0 {
		t.Errorf("score for 'Black Clover Season 2' against S1 = %d, want 0", score)
	}
}

func TestSearchQueryVariants(t *testing.T) {
	variants := searchQueryVariants("Black Clover Season 2")
	want := []string{"Black Clover Season 2", "Black Clover 2", "Black Clover"}
	if len(variants) != len(want) {
		t.Fatalf("variants = %v, want %v", variants, want)
	}
	for i, w := range want {
		if variants[i] != w {
			t.Errorf("variant[%d] = %q, want %q", i, variants[i], w)
		}
	}
}
