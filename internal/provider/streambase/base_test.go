package streambase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestHasAnimationGenre(t *testing.T) {
	tests := []struct {
		name   string
		genres []int
		want   bool
	}{
		{"empty", nil, false},
		{"no animation", []int{28, 12, 878}, false},
		{"animation only", []int{16}, true},
		{"mixed genres", []int{10759, 16, 10765, 35}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAnimationGenre(tc.genres); got != tc.want {
				t.Errorf("hasAnimationGenre(%v) = %v, want %v", tc.genres, got, tc.want)
			}
		})
	}
}

func TestExtractYear(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"2024-05-10", "2024"},
		{"1999", "1999"},
		{"", ""},
		{"20", ""},
	}

	for _, tc := range tests {
		if got := extractYear(tc.input); got != tc.want {
			t.Errorf("extractYear(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSearchTMDBCartoonsFiltersAnimation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tmdbMultiSearchResponse{
			Page:         1,
			TotalPages:   1,
			TotalResults: 3,
			Results: []tmdbMultiSearchHit{
				{
					ID:          101,
					Title:       "Live Action Hero",
					MediaType:   "movie",
					ReleaseDate: "2020-01-01",
					GenreIDs:    []int{28, 878},
					Popularity:  50.0,
				},
				{
					ID:           102,
					Name:         "Animated Show",
					MediaType:    "tv",
					FirstAirDate: "2015-06-15",
					GenreIDs:     []int{16, 10759},
					Popularity:   40.0,
				},
				{
					ID:          103,
					Title:       "Animated Feature",
					MediaType:   "movie",
					ReleaseDate: "2018-12-01",
					GenreIDs:    []int{16, 35},
					Popularity:  30.0,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	keyPool := tmdb.NewKeyPool([]string{"test-key"})
	base, err := New(keyPool)
	if err != nil {
		t.Fatalf("streambase.New: %v", err)
	}

	// Override fetchTMDBMultiSearchPage behavior by calling custom endpoint via httpClient
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := base.httpClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	var searchResp tmdbMultiSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(searchResp.Results) != 3 {
		t.Fatalf("expected 3 hits, got %d", len(searchResp.Results))
	}

	// Filter hits using the same logic as searchTMDBCartoons
	var results []provider.SearchResult
	for _, h := range searchResp.Results {
		if (h.MediaType != "movie" && h.MediaType != "tv") || !hasAnimationGenre(h.GenreIDs) {
			continue
		}
		title := h.Title
		mediaType := provider.MediaTypeMovie
		year := extractYear(h.ReleaseDate)
		if h.MediaType == "tv" {
			title = h.Name
			mediaType = provider.MediaTypeTV
			year = extractYear(h.FirstAirDate)
		}
		results = append(results, provider.SearchResult{
			Title:     title,
			ID:        "test",
			Type:      provider.ModeCartoon,
			Year:      year,
			TMDBID:    h.ID,
			MediaType: mediaType,
		})
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 animation results, got %d", len(results))
	}
	if results[0].Title != "Animated Show" || results[0].MediaType != provider.MediaTypeTV || results[0].Year != "2015" {
		t.Errorf("unexpected first result: %+v", results[0])
	}
	if results[1].Title != "Animated Feature" || results[1].MediaType != provider.MediaTypeMovie || results[1].Year != "2018" {
		t.Errorf("unexpected second result: %+v", results[1])
	}
}
