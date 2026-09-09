package anilight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
)

func TestAniLightSearch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query().Get("q")
		if q != "Solo Leveling" {
			t.Fatalf("unexpected query: %s", q)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"results": [
				{
					"id": 155,
					"slug": "solo-leveling",
					"anilistId": 151807,
					"idMal": 52299,
					"title": {
						"romaji": "Ore dake Level Up na Ken",
						"english": "Solo Leveling",
						"native": "俺だけレベルアップな件"
					},
					"seasonYear": 2024,
					"format": "TV"
				},
				{
					"id": 200,
					"slug": "anime-movie",
					"anilistId": 10001,
					"title": {
						"romaji": "Movie Romaji",
						"english": "",
						"native": ""
					},
					"startDate": { "year": 2023 },
					"format": "MOVIE"
				}
			]
		}`))
	}))
	defer ts.Close()

	c, err := NewClientWithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	results, err := c.Search(context.Background(), "Solo Leveling", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].ID != "151807" || results[0].Title != "Solo Leveling" || results[0].Year != "2024" || results[0].MediaType != provider.MediaTypeAnime {
		t.Errorf("unexpected result[0]: %+v", results[0])
	}
	if results[1].ID != "10001" || results[1].Title != "Movie Romaji" || results[1].Year != "2023" || results[1].MediaType != provider.MediaTypeMovie {
		t.Errorf("unexpected result[1]: %+v", results[1])
	}
}

func TestAniLightFetchEpisodes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/episodes/151807" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": 155,
			"slug": "solo-leveling",
			"anilistId": 151807,
			"hasSub": true,
			"hasDub": true,
			"episodes": [
				{
					"number": 1,
					"title": "I'm Used to It",
					"isFiller": false,
					"hasSub": true,
					"hasDub": true
				},
				{
					"number": 2,
					"title": "If I Had One More Chance",
					"isFiller": true,
					"hasSub": true,
					"hasDub": false
				},
				{
					"number": 0.5,
					"title": "Recap Episode",
					"hasSub": true
				}
			]
		}`))
	}))
	defer ts.Close()

	c, err := NewClientWithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	series := provider.SearchResult{ID: "151807", Title: "Solo Leveling"}
	eps, err := c.FetchEpisodes(context.Background(), series)
	if err != nil {
		t.Fatalf("FetchEpisodes() error = %v", err)
	}

	// Ep 1 has sub and dub (2 entries), Ep 2 has sub only (1 entry), Ep 0.5 skipped -> 3 total
	if len(eps) != 3 {
		t.Fatalf("expected 3 episodes, got %d: %+v", len(eps), eps)
	}

	// Ep 1 sub
	if eps[0].Episode != 1 || eps[0].Audio != "sub" || eps[0].Filler || eps[0].ID != "watch/anilight/151807/sub/1" {
		t.Errorf("unexpected eps[0]: %+v", eps[0])
	}
	// Ep 1 dub
	if eps[1].Episode != 1 || eps[1].Audio != "dub" || eps[1].Filler || eps[1].ID != "watch/anilight/151807/dub/1" {
		t.Errorf("unexpected eps[1]: %+v", eps[1])
	}
	// Ep 2 sub
	if eps[2].Episode != 2 || eps[2].Audio != "sub" || !eps[2].Filler || eps[2].ID != "watch/anilight/151807/sub/2" {
		t.Errorf("unexpected eps[2]: %+v", eps[2])
	}
}

func TestAniLightResolveSource(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/watch/l/151807/sub/1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"provider": "l",
			"category": "sub",
			"episodeNumber": 1,
			"anilistId": 151807,
			"streams": [
				{
					"url": "https://hls.example.com/master.m3u8",
					"quality": "auto",
					"type": "hls",
					"provider": "l",
					"headers": {
						"User-Agent": "Custom-UA/1.0",
						"Referer": "https://krussdomi.com/",
						"Origin": "https://krussdomi.com"
					},
					"mpv": {
						"url": "https://hls.example.com/master.m3u8",
						"args": [
							"--http-header-fields=User-Agent: Custom-UA/1.0,Referer: https://krussdomi.com/,Origin: https://krussdomi.com",
							"--demuxer-lavf-o=reconnect=1,reconnect_streamed=1,reconnect_delay_max=5",
							"--force-seekable=yes"
						]
					}
				}
			],
			"subtitles": [
				{
					"url": "https://subst.example.com/eng.vtt",
					"lang": "eng",
					"label": "English",
					"kind": "captions",
					"default": true
				},
				{
					"url": "https://subst.example.com/spa.vtt",
					"lang": "spa",
					"label": "Spanish",
					"kind": "captions",
					"default": false
				},
				{
					"url": "https://subst.example.com/preview.vtt",
					"lang": "thumbnails",
					"label": "Thumbnails",
					"kind": "thumbnails",
					"default": false
				}
			]
		}`))
	}))
	defer ts.Close()

	c, err := NewClientWithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	ep := provider.Episode{
		Episode: 1,
		Audio:   "sub",
		ID:      "watch/anilight/151807/sub/1",
	}
	sources, err := c.ResolveSource(context.Background(), "151807", ep)
	if err != nil {
		t.Fatalf("ResolveSource() error = %v", err)
	}

	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}

	src := sources[0]
	if src.URL != "https://hls.example.com/master.m3u8" {
		t.Errorf("unexpected URL: %s", src.URL)
	}
	if src.Referer != "https://krussdomi.com/" {
		t.Errorf("unexpected Referer: %s", src.Referer)
	}
	if src.UserAgent != "Custom-UA/1.0" {
		t.Errorf("unexpected UserAgent: %s", src.UserAgent)
	}
	if src.Quality != "Auto (l)" {
		t.Errorf("unexpected Quality: %s", src.Quality)
	}
	if len(src.ExtraArgs) == 0 {
		t.Errorf("expected ExtraArgs from MPV, got none")
	}

	// Subtitles check: thumbnails must be filtered out, leaving 2 tracks (en, es)
	if len(src.Subtitles) != 2 {
		t.Fatalf("expected 2 subtitle tracks, got %d: %+v", len(src.Subtitles), src.Subtitles)
	}
	if src.Subtitles[0].Language != "en" || src.Subtitles[0].URL != "https://subst.example.com/eng.vtt" {
		t.Errorf("unexpected sub[0]: %+v", src.Subtitles[0])
	}
	if src.Subtitles[1].Language != "es" || src.Subtitles[1].URL != "https://subst.example.com/spa.vtt" {
		t.Errorf("unexpected sub[1]: %+v", src.Subtitles[1])
	}
}
