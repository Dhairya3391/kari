package anikoto

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
)

func TestAnikotoSearch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query().Get("q")
		if q != "Solo Leveling" {
			t.Fatalf("unexpected query: %s", q)
		}
		w.Header().Set("Content-Type", "application/json")
		// Test both int and string IDs
		w.Write([]byte(`{
			"results": [
				{"id": 151807, "name": "Solo Leveling", "format": "TV", "year": 2024},
				{"id": "184694", "name": "Solo Leveling Movie", "format": "MOVIE", "year": 2024}
			]
		}`))
	}))
	defer ts.Close()

	client, err := NewClientWithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL: %v", err)
	}

	results, err := client.Search(context.Background(), "Solo Leveling", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].ID != "151807" || results[0].MediaType != provider.MediaTypeAnime || results[0].Year != "2024" {
		t.Errorf("unexpected results[0]: %+v", results[0])
	}
	if results[1].ID != "184694" || results[1].MediaType != provider.MediaTypeMovie || results[1].Year != "2024" {
		t.Errorf("unexpected results[1]: %+v", results[1])
	}
}

func TestAnikotoFetchEpisodes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/episodes/151807" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"id": "watch/anikoto/151807/sub/1", "number": 1, "category": "sub", "title": "Ep 1", "filler": false},
			{"id": "watch/anikoto/151807/dub/1", "number": 1, "category": "dub", "title": "Ep 1", "filler": false},
			{"id": "watch/anikoto/151807/sub/2", "number": 2, "category": "sub", "title": "Ep 2", "filler": true},
			{"id": "watch/anikoto/151807/sub/0.5", "number": 0.5, "category": "sub", "title": "Recap", "filler": true},
			{"id": "watch/anikoto/151807/sub/0", "number": 0, "category": "sub", "title": "Zero", "filler": true}
		]`))
	}))
	defer ts.Close()

	client, err := NewClientWithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL: %v", err)
	}

	eps, err := client.FetchEpisodes(context.Background(), provider.SearchResult{ID: "151807"})
	if err != nil {
		t.Fatalf("FetchEpisodes failed: %v", err)
	}

	// 0 and 0.5 should be filtered out; 1 (sub), 1 (dub), 2 (sub) remain = 3
	if len(eps) != 3 {
		t.Fatalf("expected 3 episodes, got %d", len(eps))
	}
	if eps[0].Episode != 1 || eps[0].Audio != "sub" || eps[0].Filler != false {
		t.Errorf("unexpected eps[0]: %+v", eps[0])
	}
	if eps[1].Episode != 1 || eps[1].Audio != "dub" || eps[1].Filler != false {
		t.Errorf("unexpected eps[1]: %+v", eps[1])
	}
	if eps[2].Episode != 2 || eps[2].Audio != "sub" || eps[2].Filler != true {
		t.Errorf("unexpected eps[2]: %+v", eps[2])
	}
}

func TestAnikotoResolveSource(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/link" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("id") != "watch/anikoto/151807/sub/1" {
			t.Fatalf("unexpected id: %s", r.URL.Query().Get("id"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"streams": [
				{
					"type": "hls",
					"url": "https://cdn.example.com/master.m3u8",
					"quality": "1080p",
					"server": "Vidstream-2",
					"provider": "anikoto",
					"priority": 1,
					"headers": {
						"Referer": "https://megaplay.buzz/",
						"User-Agent": "Custom-UA"
					}
				},
				{
					"type": "hls",
					"url": "http://proxy.example.com/master.m3u8",
					"quality": "1080p",
					"server": "HD-1 (Proxied)",
					"provider": "anikoto",
					"priority": 2,
					"referer": "https://megaplay.buzz/"
				}
			],
			"subtitles": [
				{
					"file": "https://cdn.example.com/sub.vtt",
					"label": "English",
					"language": "en"
				}
			]
		}`))
	}))
	defer ts.Close()

	client, err := NewClientWithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL: %v", err)
	}

	sources, err := client.ResolveSource(context.Background(), "151807", provider.Episode{ID: "watch/anikoto/151807/sub/1"})
	if err != nil {
		t.Fatalf("ResolveSource failed: %v", err)
	}

	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}

	s1 := sources[0]
	if s1.URL != "https://cdn.example.com/master.m3u8" {
		t.Errorf("expected URL https://cdn.example.com/master.m3u8, got %s", s1.URL)
	}
	if s1.Quality != "1080p (Vidstream-2)" {
		t.Errorf("expected quality '1080p (Vidstream-2)', got %s", s1.Quality)
	}
	if s1.Referer != "https://megaplay.buzz/" {
		t.Errorf("expected referer https://megaplay.buzz/, got %s", s1.Referer)
	}
	if s1.UserAgent != "Custom-UA" {
		t.Errorf("expected user-agent Custom-UA, got %s", s1.UserAgent)
	}
	if len(s1.Subtitles) != 1 || s1.Subtitles[0].URL != "https://cdn.example.com/sub.vtt" {
		t.Errorf("unexpected subtitles: %+v", s1.Subtitles)
	}
}
