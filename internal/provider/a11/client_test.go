package a11

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestA11Metadata(t *testing.T) {
	keyPool := tmdb.NewKeyPool([]string{"dummy-key"})
	c, err := NewClient(keyPool)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	if c.Name() != "a11" {
		t.Errorf("expected a11, got %s", c.Name())
	}
	if c.Alias() != "111477" {
		t.Errorf("expected 111477, got %s", c.Alias())
	}

	modes := c.Modes()
	if len(modes) != 2 {
		t.Fatalf("expected 2 modes, got %d", len(modes))
	}
	if modes[0].Name != provider.ModeMovies || modes[1].Name != provider.ModeTV {
		t.Errorf("unexpected modes: %+v", modes)
	}
}

func TestExtractQuality(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "The.Dark.Knight.2008.UHD.BluRay.2160p.DTS-HD.MA.5.1.HEVC.REMUX-FraMeSToR.mkv [a11 54.3 GB]",
			want:  "2160P [Remux 54.3 GB]",
		},
		{
			input: "The.Dark.Knight.2008.INTERNAL.IMAX.2160p.UHD.BluRay.x265.HDR.DD5.1-Pahe.in.mkv [a11 5.3 GB]",
			want:  "2160P [IMAX 5.3 GB]",
		},
		{
			input: "Breaking.Bad.S01E01.1080p.BluRay.x264.mkv 2.1 GB",
			want:  "1080P [BluRay 2.1 GB]",
		},
		{
			input: "Unknown Title",
			want:  "2160P [Direct]",
		},
	}

	for _, tc := range tests {
		got := extractQuality(tc.input)
		if got != tc.want {
			t.Errorf("extractQuality(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestA11ResolveStreamMovie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/movie/155" {
			fmt.Fprintf(w, `{"imdb_id": "tt0468569"}`)
			return
		}
		fmt.Fprintf(w, `{
			"streams": [
				{
					"name": "111477",
					"title": "The.Dark.Knight.2008.UHD.BluRay.2160p.REMUX.mkv [54.3 GB]",
					"url": "https://young-hill-365c.bdegd.workers.dev/d/sample1"
				},
				{
					"name": "111477",
					"title": "The.Dark.Knight.2008.1080p.BluRay.mkv [12.1 GB]",
					"url": "https://young-hill-365c.bdegd.workers.dev/d/sample2"
				}
			]
		}`)
	}))
	defer srv.Close()

	keyPool := tmdb.NewKeyPool([]string{"dummy-key"})
	client, err := NewClientWithURLs(keyPool, srv.URL, srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewClientWithURLs failed: %v", err)
	}
	client.SetTMDBBaseURL(srv.URL)

	updates := make(chan []provider.MediaSource, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.ResolveStream(ctx, "155", provider.Episode{TMDBID: 155}, updates)
	if err != nil {
		t.Fatalf("ResolveStream failed: %v", err)
	}
	close(updates)

	var sources []provider.MediaSource
	for batch := range updates {
		sources = append(sources, batch...)
	}

	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}
	if sources[0].Quality != "2160P [Remux 54.3 GB]" {
		t.Errorf("expected 2160P [Remux 54.3 GB], got %s", sources[0].Quality)
	}
	if sources[0].URL != "https://young-hill-365c.bdegd.workers.dev/d/sample1" {
		t.Errorf("unexpected URL: %s", sources[0].URL)
	}
}

func TestA11ResolveSourceNoStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"streams": []}`)
	}))
	defer srv.Close()

	keyPool := tmdb.NewKeyPool([]string{"dummy-key"})
	client, err := NewClientWithURLs(keyPool, srv.URL, srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("NewClientWithURLs failed: %v", err)
	}

	_, err = client.ResolveSource(context.Background(), "999999", provider.Episode{TMDBID: 999999})
	if err == nil {
		t.Fatal("expected error on empty streams, got nil")
	}
}
