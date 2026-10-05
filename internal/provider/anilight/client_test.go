package anilight

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

// Canned Megaplay enc token; decrypts to a nexabloom master.m3u8.
const fixtureEnc = "wdeBruh3qqn_i5wUNnyaPcXqidp1UWP84FfPHzGyKXDMoY_RzXqbC0h49XmoI7d0vYZArA5rcKY-FQxnEk8NNEWezec9dd2jwxp1UbLN43p_7CMxXPDF5BUX86bUm0_Uuw0-dlv_yT9MKsnmlOgDFm3ReCPZPJloNlqBF7aftTk"

// newDirectServer serves AniList GraphQL at / and the AniLight site API
// (check-exists, watch, sources, megaplay embed + getSources). All four
// named providers fail so resolution exercises the Megaplay embed fallback.
func newDirectServer(t *testing.T) *httptest.Server {
	t.Helper()
	var base string
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "Media(") {
			fmt.Fprint(w, `{"data":{"Media":{"id":151807,"idMal":52299,"title":{"romaji":"Ore dake Level Up na Ken","english":"Solo Leveling","userPreferred":"Solo Leveling","native":"X"},"format":"TV","status":"FINISHED","seasonYear":2024,"episodes":3,"nextAiringEpisode":null}}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"Page":{"media":[
			{"id":151807,"title":{"romaji":"Ore dake Level Up na Ken","english":"Solo Leveling","userPreferred":"Solo Leveling","native":"X"},"seasonYear":2024,"format":"TV"},
			{"id":10001,"title":{"romaji":"Movie Romaji","english":"","userPreferred":"","native":""},"seasonYear":2023,"format":"MOVIE"}
		]}}}`)
	})

	mux.HandleFunc("/anime/check-exists", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("anilistId") == "151807" {
			fmt.Fprint(w, `{"exists":true,"slug":"solo-leveling"}`)
			return
		}
		fmt.Fprint(w, `{"exists":false}`)
	})

	mux.HandleFunc("/watch/solo-leveling", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":155,"episodes":[
			{"number":1,"title":"I'm Used to It","embed_url":{"sub":%q,"dub":%q}},
			{"number":2,"title":"Second","embed_url":{"sub":%q}}
		]}`,
			base+"/stream/s-2/107257/sub", base+"/stream/s-2/107257/dub", base+"/stream/s-2/107258/sub")
	})

	// Named providers all fail; the embed fallback must carry resolution.
	mux.HandleFunc("/sources", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no source", http.StatusInternalServerError)
	})

	// Megaplay embed page (file ID comes from the URL path).
	mux.HandleFunc("/stream/s-2/107257/sub", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>embed</body></html>`)
	})
	mux.HandleFunc("/stream/s-2/107257/dub", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>embed</body></html>`)
	})
	mux.HandleFunc("/stream/getSources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "107257" {
			http.Error(w, "unknown file", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"enc":%q,"tracks":[{"file":"https://subst.example.com/thumb.webp","label":"English","kind":"thumbnails"},{"file":"https://subst.example.com/eng.vtt","label":"English","default":true}]}`,
			fixtureEnc)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	return srv
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := NewClientWithBaseURL(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL: %v", err)
	}
	return c
}

func TestAniLightSearch(t *testing.T) {
	srv := newDirectServer(t)
	c := newTestClient(t, srv)

	results, err := c.Search(context.Background(), "Solo Leveling", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "151807" || results[0].Title != "Solo Leveling" || results[0].Year != "2024" {
		t.Errorf("unexpected result[0]: %+v", results[0])
	}
	if results[1].ID != "10001" || results[1].MediaType != provider.MediaTypeMovie {
		t.Errorf("unexpected result[1]: %+v", results[1])
	}
}

func TestAniLightFetchEpisodes(t *testing.T) {
	srv := newDirectServer(t)
	c := newTestClient(t, srv)

	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "151807", Title: "Solo Leveling"})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	// Ep 1 sub+dub, ep 2 sub-only = 3.
	if len(eps) != 3 {
		t.Fatalf("expected 3 episodes, got %+v", eps)
	}
	if eps[0].Episode != 1 || eps[0].Audio != "sub" || eps[0].ID != "watch/anilight/151807/sub/1" {
		t.Errorf("unexpected eps[0]: %+v", eps[0])
	}
	if eps[1].Episode != 1 || eps[1].Audio != "dub" {
		t.Errorf("unexpected eps[1]: %+v", eps[1])
	}
	if eps[2].Episode != 2 || eps[2].Audio != "sub" {
		t.Errorf("unexpected eps[2]: %+v", eps[2])
	}
}

func TestAniLightFetchEpisodesAnilistFallback(t *testing.T) {
	srv := newDirectServer(t)
	c := newTestClient(t, srv)

	// Unknown to the site API; the AniList count (3) seeds sub+dub = 6.
	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "999999"})
	if err != nil {
		t.Fatalf("FetchEpisodes fallback: %v", err)
	}
	if len(eps) != 6 {
		t.Fatalf("expected 6 synthetic episodes, got %d", len(eps))
	}
}

func TestAniLightResolveSource(t *testing.T) {
	srv := newDirectServer(t)
	c := newTestClient(t, srv)

	ep := provider.Episode{Episode: 1, Audio: "sub", ID: "watch/anilight/151807/sub/1"}
	sources, err := c.ResolveSource(context.Background(), "151807", ep)
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	src := sources[0]
	if !strings.HasPrefix(src.URL, "https://fetch.nexabloom.top/") || !strings.Contains(src.URL, "?token=") {
		t.Errorf("expected signed direct CDN URL, got %s", src.URL)
	}
	if strings.Contains(src.URL, "127.0.0.1") || strings.Contains(src.URL, "localhost") || strings.Contains(src.URL, "/proxy/") {
		t.Errorf("source must never be a proxy URL: %s", src.URL)
	}
	if src.Type != provider.SourceTypeHLS {
		t.Errorf("expected hls type, got %s", src.Type)
	}
	if src.Referer == "" || src.UserAgent == "" {
		t.Errorf("mpv needs referer + user-agent: %+v", src)
	}
	if len(src.Subtitles) != 1 ||
		src.Subtitles[0].URL != "https://subst.example.com/eng.vtt" ||
		!src.Subtitles[0].Default {
		t.Errorf("unexpected subtitles: %+v", src.Subtitles)
	}
}

func TestAniLightResolveDubUnavailable(t *testing.T) {
	srv := newDirectServer(t)
	c := newTestClient(t, srv)

	// Episode 2 in fixture has no dub embed URL. Must return ErrAudioUnavailable.
	ep := provider.Episode{Episode: 2, Audio: "dub", ID: "watch/anilight/151807/dub/2"}
	_, err := c.ResolveSource(context.Background(), "151807", ep)
	if !errors.Is(err, provider.ErrAudioUnavailable) {
		t.Errorf("dub resolve for missing dub embed must return ErrAudioUnavailable, got %v", err)
	}
}

// TestAniLightAggregatesAllBackends pins that resolution collects every
// healthy backend instead of stopping at the first: backend health rotates
// run to run, and first-success returns made rows vanish on refresh.
func TestAniLightAggregatesAllBackends(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/anime/check-exists", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"exists":true,"slug":"solo-leveling"}`)
	})
	mux.HandleFunc("/watch/solo-leveling", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":155,"episodes":[{"number":1,"title":"One","embed_url":{"sub":"https://embed.example.com/s"}}]}`)
	})
	mux.HandleFunc("/sources", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pid := r.URL.Query().Get("providerId")
		if pid == "ryu" {
			http.Error(w, "backend down", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"sources":[{"url":"https://cdn.example.com/%s.m3u8","quality":"1080p"}],"tracks":[]}`, pid)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv)

	ep := provider.Episode{Episode: 1, Audio: "sub", ID: "watch/anilight/151807/sub/1"}
	sources, err := c.ResolveSource(context.Background(), "151807", ep)
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	// mello, vid, l deliver; ryu fails. All three must be present.
	if len(sources) != 3 {
		t.Fatalf("sources = %d, want 3 (one per healthy backend)", len(sources))
	}
	seen := make(map[string]bool)
	for _, s := range sources {
		seen[s.URL] = true
	}
	for _, pid := range []string{"mello", "vid", "l"} {
		if !seen["https://cdn.example.com/"+pid+".m3u8"] {
			t.Errorf("backend %q missing from sources: %v", pid, sources)
		}
	}
}
