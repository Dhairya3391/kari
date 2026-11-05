package stability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kari/internal/provider"
)

func TestStabilityMetadata(t *testing.T) {
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Name() != "stability" {
		t.Errorf("Name = %q, want stability", c.Name())
	}
	if c.Alias() != "Stability" {
		t.Errorf("Alias = %q, want Stability", c.Alias())
	}
	modes := c.Modes()
	if len(modes) != 1 || modes[0].Name != provider.ModeLive {
		t.Errorf("Modes = %+v, want ModeLive", modes)
	}
	feat := c.Features(provider.ModeLive)
	if !feat.AllowEmptyQuery || !feat.NoCachedSearches {
		t.Errorf("Features(ModeLive) = %+v, want AllowEmptyQuery and NoCachedSearches", feat)
	}
	if c.Features(provider.ModeTV) != (provider.Features{}) {
		t.Errorf("Features(ModeTV) should be empty")
	}
	dl := c.Deadlines(provider.ModeLive)
	if dl.Source != 40*time.Second {
		t.Errorf("Deadlines(ModeLive) = %+v, want 40s source deadline", dl)
	}
}

func TestStabilitySearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/schedule", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]stabilityEvent{
			{
				ID:        "event_1",
				Title:     "Arsenal vs Chelsea",
				Slug:      "arsenal-vs-chelsea",
				Category:  "Premier League",
				Team1:     "Arsenal",
				Team2:     "Chelsea",
				Timestamp: time.Now().Add(-10 * time.Minute).UnixMilli(),
				Poster:    "https://example.com/poster.jpg",
				Channels: []stabilityChannel{
					{ID: "src_1", Name: "Sky Sports", HID: "hid_1", Language: "English"},
				},
				Sources: []stabilitySource{
					{Name: "Sky Sports Main", URL: "https://stream.example.com/live.m3u8", HID: "hid_1", Language: "English"},
				},
				M3U8s: []string{"https://stream.example.com/backup.m3u8"},
			},
			{
				ID:        "event_2",
				Title:     "Lakers vs Warriors",
				Slug:      "lakers-vs-warriors",
				Category:  "NBA",
				Team1:     "Lakers",
				Team2:     "Warriors",
				Timestamp: time.Now().Add(2 * time.Hour).UnixMilli(),
			},
		})
	})

	mux.HandleFunc("/api/channels", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(strings.ToLower(q), "sky") {
			_ = json.NewEncoder(w).Encode(channelSearchResponse{
				Channels: []channelSearchItem{
					{ID: "ch_sky", Name: "Sky Sports F1", Icon: "https://example.com/icon.png"},
				},
				Total: 1,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(channelSearchResponse{Channels: []channelSearchItem{}, Total: 0})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClientWithURL(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	// 1. Empty query should browse schedule
	all, err := client.Search(context.Background(), "", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search empty: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d results, want 2", len(all))
	}
	if all[0].ID != "stability:event:event_1" || all[0].Title != "Arsenal vs Chelsea" {
		t.Errorf("all[0] = %+v", all[0])
	}
	if !all[0].Live {
		t.Errorf("event_1 started 10m ago, expected Live=true")
	}
	if all[1].Live {
		t.Errorf("event_2 starts in 2h, expected Live=false")
	}

	// 2. Query matching event team
	chelsea, err := client.Search(context.Background(), "chelsea", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search chelsea: %v", err)
	}
	if len(chelsea) != 1 || chelsea[0].ID != "stability:event:event_1" {
		t.Errorf("chelsea = %+v", chelsea)
	}

	// 3. Query matching channel
	sky, err := client.Search(context.Background(), "sky", provider.ModeLive)
	if err != nil {
		t.Fatalf("Search sky: %v", err)
	}
	// Should match channel Sky Sports F1
	var foundChannel bool
	for _, res := range sky {
		if res.ID == "stability:channel:ch_sky" {
			foundChannel = true
			if res.Group != "Channel" || !res.Live {
				t.Errorf("channel metadata = %+v", res)
			}
		}
	}
	if !foundChannel {
		t.Errorf("expected Sky Sports F1 channel in results: %+v", sky)
	}

	// 4. Non-matching search
	_, err = client.Search(context.Background(), "nonexistent", provider.ModeLive)
	if !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("Search nonexistent err = %v, want ErrNoResults", err)
	}

	// 5. Wrong mode
	_, err = client.Search(context.Background(), "", provider.ModeTV)
	if !errors.Is(err, provider.ErrNoResults) {
		t.Errorf("Search ModeTV err = %v, want ErrNoResults", err)
	}
}

func TestStabilityFetchEpisodes(t *testing.T) {
	c, _ := NewClient()
	_, err := c.FetchEpisodes(context.Background(), provider.SearchResult{})
	if !errors.Is(err, provider.ErrNoEpisodes) {
		t.Errorf("FetchEpisodes(empty) err = %v, want ErrNoEpisodes", err)
	}

	eps, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "stability:event:123", Title: "Event 123"})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(eps) != 1 || eps[0].ID != "stability:event:123" || eps[0].Title != "Event 123" {
		t.Errorf("eps = %+v", eps)
	}
}

func TestStabilityResolveSource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/schedule", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]stabilityEvent{
			{
				ID:    "event_123",
				Title: "Match",
				Sources: []stabilitySource{
					{Name: "Direct Source", URL: "https://cdn.example.com/direct.m3u8", HID: "hid_test"},
				},
				M3U8s: []string{"https://cdn.example.com/m3u8.m3u8"},
			},
		})
	})

	mux.HandleFunc("/api/fetch", func(w http.ResponseWriter, r *http.Request) {
		slug := r.URL.Query().Get("slug")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(streamFetchResponse{
			Status: "ok",
			URL:    fmt.Sprintf("https://cdn.example.com/fetch_%s.m3u8", slug),
			Streams: []streamFetchItem{
				{MediaURL: fmt.Sprintf("https://cdn.example.com/media_%s.m3u8", slug)},
			},
		})
	})

	mux.HandleFunc("/api/warm", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(warmResponse{
			Status:    "ok",
			MasterURL: fmt.Sprintf("https://cdn.example.com/warm_master_%s.m3u8", id),
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClientWithURL(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	// 1. Resolve event
	sources, err := client.ResolveSource(context.Background(), "stability:event:event_123", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource event: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("expected at least 1 source")
	}

	var hasDirect, hasFetch bool
	for _, s := range sources {
		if s.URL == "https://cdn.example.com/direct.m3u8" {
			hasDirect = true
		}
		if s.URL == "https://cdn.example.com/fetch_hid_test.m3u8" {
			hasFetch = true
		}
		if s.Type != provider.SourceTypeHLS {
			t.Errorf("source.Type = %q, want HLS", s.Type)
		}
		if s.Referer != srv.URL+"/" {
			t.Errorf("source.Referer = %q, want %q", s.Referer, srv.URL+"/")
		}
	}
	if !hasDirect {
		t.Errorf("missing direct URL in sources: %+v", sources)
	}
	if !hasFetch {
		t.Errorf("missing fetch URL in sources: %+v", sources)
	}

	// 2. Resolve channel
	chanSources, err := client.ResolveSource(context.Background(), "stability:channel:ch_999", provider.Episode{})
	if err != nil {
		t.Fatalf("ResolveSource channel: %v", err)
	}
	if len(chanSources) == 0 {
		t.Fatal("expected channel sources")
	}
	var hasWarmMaster bool
	for _, s := range chanSources {
		if s.URL == "https://cdn.example.com/warm_master_ch_999.m3u8" {
			hasWarmMaster = true
		}
	}
	if !hasWarmMaster {
		t.Errorf("missing warm master URL in channel sources: %+v", chanSources)
	}

	// 3. Empty ID should fail
	_, err = client.ResolveSource(context.Background(), "stability:", provider.Episode{})
	if !errors.Is(err, provider.ErrNoSources) {
		t.Errorf("empty ID err = %v, want ErrNoSources", err)
	}
}

func TestStabilityErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClientWithURL(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	_, err = client.Search(context.Background(), "", provider.ModeLive)
	if err == nil {
		t.Fatal("expected error on 500 response for empty search")
	}
	_, err = client.Search(context.Background(), "arsenal", provider.ModeLive)
	if err == nil {
		t.Fatal("expected error on 500 response for filtered search when both backends fail")
	}
}

func TestStabilityContextCancellation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/schedule", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClientWithURL(srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewClientWithURL: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = client.ResolveSource(ctx, "stability:event:ev_1", provider.Episode{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ResolveSource with cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestStabilityTimestampParsing(t *testing.T) {
	// Millisecond timestamp (~2026)
	msTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	evMS := stabilityEvent{Timestamp: msTime.UnixMilli()}
	gotMS := parseEventStart(evMS)
	if gotMS.Unix() != msTime.Unix() {
		t.Errorf("parseEventStart(millis) = %v, want %v", gotMS.Unix(), msTime.Unix())
	}

	// Second timestamp (~2026)
	secTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	evSec := stabilityEvent{Timestamp: secTime.Unix()}
	gotSec := parseEventStart(evSec)
	if gotSec.Unix() != secTime.Unix() {
		t.Errorf("parseEventStart(seconds) = %v, want %v", gotSec.Unix(), secTime.Unix())
	}
}
