package reanime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
)

func TestReAnimeSearch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query().Get("q")
		if q != "Bleach" {
			t.Fatalf("unexpected query: %s", q)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"results": [
				{
					"id": 159322,
					"name": "Bleach: Thousand-Year Blood War - The Conflict",
					"format": "TV",
					"year": 2024
				}
			]
		}`))
	}))
	defer ts.Close()

	c, err := NewClientWithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	results, err := c.Search(context.Background(), "Bleach", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "159322" || results[0].Title != "Bleach: Thousand-Year Blood War - The Conflict" || results[0].Year != "2024" {
		t.Errorf("unexpected result: %+v", results[0])
	}
}

func TestReAnimeFetchEpisodes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/episodes/159322" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{
				"id": "watch/hd1/159322/sub/1",
				"number": 1,
				"category": "sub",
				"title": "Episode 1",
				"filler": false,
				"provider": "hd1"
			},
			{
				"id": "watch/hd2/159322/sub/1",
				"number": 1,
				"category": "sub",
				"title": "Episode 1",
				"filler": false,
				"provider": "hd2"
			},
			{
				"id": "watch/hd1/159322/dub/1",
				"number": 1,
				"category": "dub",
				"title": "Episode 1",
				"filler": false,
				"provider": "hd1"
			}
		]`))
	}))
	defer ts.Close()

	c, err := NewClientWithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}

	series := provider.SearchResult{ID: "159322", Title: "Bleach"}
	eps, err := c.FetchEpisodes(context.Background(), series)
	if err != nil {
		t.Fatalf("FetchEpisodes() error = %v", err)
	}
	// duplicate sub episode between hd1 and hd2 should be deduped -> 2 unique episodes (1 sub, 1 dub)
	if len(eps) != 2 {
		t.Fatalf("expected 2 unique episodes, got %d: %+v", len(eps), eps)
	}
	if eps[0].Audio != "sub" || eps[1].Audio != "dub" {
		t.Errorf("unexpected episode audio order: %+v", eps)
	}
}

func TestReAnimeResolveSource(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/watch/hd1/159322/sub/1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"streams": [
				{
					"type": "hls",
					"url": "https://noob4.broggl.farm/proxy/m3u8?url=https://fetch9.flixcloud.cc/master.m3u8",
					"directUrl": "https://fetch9.flixcloud.cc/master.m3u8",
					"quality": "auto",
					"server": "HD1",
					"provider": "hd1",
					"referer": "https://flixcloud.cc/",
					"headers": {
						"Referer": "https://flixcloud.cc/",
						"User-Agent": "ReAnime-UA"
					},
					"mpv": {
						"url": "https://noob4.broggl.farm/proxy/m3u8?url=...",
						"args": [
							"--http-header-fields=User-Agent: ReAnime-UA",
							"--http-header-fields=Referer: https://flixcloud.cc/"
						]
					}
				}
			],
			"subtitles": []
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
		ID:      "watch/hd1/159322/sub/1",
	}
	sources, err := c.ResolveSource(context.Background(), "159322", ep)
	if err != nil {
		t.Fatalf("ResolveSource() error = %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].Quality != "Auto (HD1)" {
		t.Errorf("unexpected quality: %s", sources[0].Quality)
	}
	if sources[0].Referer != "https://flixcloud.cc/" {
		t.Errorf("unexpected referer: %s", sources[0].Referer)
	}
}
