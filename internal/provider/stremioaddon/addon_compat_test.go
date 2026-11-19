package stremioaddon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestNormalizeManifestURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"bare root", "https://cncverse.dpdns.org/", "https://cncverse.dpdns.org/manifest.json"},
		{"bare host", "https://cncverse.dpdns.org", "https://cncverse.dpdns.org/manifest.json"},
		{"configured query", "https://addon.example/base?token=abc", "https://addon.example/base/manifest.json?token=abc"},
		{"already manifest", "https://addon.example/manifest.json?token=abc", "https://addon.example/manifest.json?token=abc"},
		{"uppercase scheme", "HTTPS://Addon.Example/manifest.json?token=abc", "https://Addon.Example/manifest.json?token=abc"},
		{"fragment dropped", "https://addon.example/manifest.json#top", "https://addon.example/manifest.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeManifestURL(tt.raw)
			if err != nil {
				t.Fatalf("normalizeManifestURL(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeManifestURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
	if _, err := normalizeManifestURL("ftp://addon.example/manifest.json"); err == nil {
		t.Fatal("normalizeManifestURL accepted a non-HTTP URL")
	}
}

func TestStreamQualityFromBridgeManifest(t *testing.T) {
	tests := []struct {
		name   string
		item   streamItem
		label  string
		height int
	}{
		{
			name: "verbose live title with filename",
			item: streamItem{
				Title: "✅ Buffer-free · auto quality\n🏏 PAK vs SL\n🏷️ Auto • HLS",
				Behavior: streamBehavior{
					Filename:   "PAK.vs.SL.1080p.WEB-DL-SportzX",
					BingeGroup: "cnc-sportzx-1080p",
				},
			},
			label: "1080p", height: 1080,
		},
		{
			name:  "binge group only",
			item:  streamItem{Title: "multi\nline", Behavior: streamBehavior{BingeGroup: "cnc-sportzx-396p"}},
			label: "396p", height: 396,
		},
		{
			name:  "short label preserved",
			item:  streamItem{Title: "1080p WEB"},
			label: "1080p WEB", height: 1080,
		},
		{
			name:  "name only",
			item:  streamItem{Name: "1080p"},
			label: "1080p", height: 1080,
		},
		{
			name:  "generic name does not hide filename resolution",
			item:  streamItem{Name: "Provider", Behavior: streamBehavior{Filename: "match.720p.m3u8"}},
			label: "720p", height: 720,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, height := streamQuality(tt.item)
			if label != tt.label || height != tt.height {
				t.Fatalf("streamQuality() = %q/%d, want %q/%d", label, height, tt.label, tt.height)
			}
		})
	}
}

func TestBridgeAddonCatalogAndSources(t *testing.T) {
	const liveID = "cnc:U3BvcnR6WExpdmVW:test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprint(w, `{"id":"com.cnc.stremiobridge","name":"CNCVerse Bridge","types":["movie","series","other","tv"],"resources":["catalog","meta","stream","subtitles"],"catalogs":[{"type":"tv","id":"cnc_live_tv"}],"behaviorHints":{"configurable":true}}`)
		case "/catalog/tv/cnc_live_tv.json":
			fmt.Fprintf(w, `{"metas":[{"id":%q,"type":"tv","name":"PAK vs SL","year":"2026"}]}`, liveID)
		case "/stream/tv/" + url.PathEscape(liveID) + ".json":
			fmt.Fprint(w, `{"streams":[
				{"url":"https://cdn.example/live.m3u8","name":"Provider","title":"✅ Buffer-free\n🏏 Auto • HLS","behaviorHints":{"filename":"match.1080p.WEB-DL","proxyHeaders":{"request":{"Referer":"https://provider.example/","Origin":"https://provider.example","X-CNC":"1"}}}},
				{"url":"https://cdn.example/drm.m3u8","name":"Provider","title":"720p","clearkey":"abc:def"}
			]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(context.Background(), server.URL+"/", tmdb.NewKeyPool(nil))
	if err != nil {
		t.Fatalf("NewClient(bare URL) error = %v", err)
	}
	results, err := client.Search(context.Background(), "", provider.ModeStremio)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].Year != "2026" {
		t.Fatalf("Search() year = %+v, want 2026 from the year field", results)
	}
	sources, err := client.ResolveSource(context.Background(), results[0].ID, provider.Episode{ID: liveID})
	if err != nil {
		t.Fatalf("ResolveSource() error = %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("ResolveSource() returned %d sources, want 2", len(sources))
	}
	if sources[0].Quality != "1080p" || sources[0].Height != 1080 {
		t.Fatalf("first source quality = %q/%d, want 1080p/1080", sources[0].Quality, sources[0].Height)
	}
	if sources[0].ExtraHeaders["X-Cnc"] != "1" {
		t.Fatalf("extra headers = %+v, want Origin-adjacent headers preserved", sources[0].ExtraHeaders)
	}
	if !sources[1].FallbackOnly {
		t.Fatal("clearkey stream must be marked FallbackOnly")
	}
}

func TestCatalogSuppliesRequiredPaginationExtras(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/manifest.json" {
			fmt.Fprint(w, `{"id":"test","name":"Test","resources":["catalog"],`+
				`"catalogs":[{"type":"tv","id":"req","extra":[`+
				`{"name":"skip","isRequired":true},{"name":"page","isRequired":true}]}]}`)
			return
		}
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"metas":[]}`)
	}))
	defer server.Close()

	client, err := NewClient(context.Background(), server.URL+"/", tmdb.NewKeyPool(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), "", provider.ModeStremio); err != nil &&
		!errors.Is(err, provider.ErrNoResults) {
		t.Fatalf("Search() error = %v", err)
	}
	const want = "/catalog/tv/req/page=1&skip=0.json"
	if gotPath != want {
		t.Fatalf("catalog path = %q, want %q", gotPath, want)
	}
}

func TestManifestFetchErrorIsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := NewClientWithHTTP(context.Background(), server.URL+"/manifest.json?token=very-secret", tmdb.NewKeyPool(nil), server.Client())
	if err == nil {
		t.Fatal("NewClient accepted a failing manifest endpoint")
	}
	if strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("manifest error leaked the configured token: %v", err)
	}
	if !strings.Contains(err.Error(), "http status 500") {
		t.Fatalf("manifest error lost the status code: %v", err)
	}
}

func TestCatalogURLWithExtras(t *testing.T) {
	c := &Client{endpoint: "https://addon.example", query: "token=abc"}
	got, err := c.catalogURLWithExtras("catalog", "tv", "cnc:live", map[string]string{
		"search": "TOM & JERRY=1",
		"skip":   "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://addon.example/catalog/tv/cnc:live/" +
		"search=TOM%20%26%20JERRY%3D1&skip=0.json?token=abc"
	if got != want {
		t.Fatalf("catalogURLWithExtras() = %q, want %q", got, want)
	}
}
