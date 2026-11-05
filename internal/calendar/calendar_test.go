package calendar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"kari/internal/history"
	"kari/internal/provider"
	"kari/internal/tmdb"
)

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Sousou no Frieren", "sousou no frieren"},
		{"Frieren: Beyond Journey's End", "frieren beyond journey s end"},
		{"  Attack on Titan (Season 4)  ", "attack on titan season 4"},
		{"Solo Leveling!", "solo leveling"},
	}

	for _, tc := range tests {
		got := NormalizeTitle(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestCrossReference_Anime(t *testing.T) {
	histPath := filepath.Join(t.TempDir(), "history.json")
	store, err := history.NewStore(histPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 1. Frieren: user watched up to ep 5 complete
	for ep := 1; ep <= 5; ep++ {
		_ = store.Upsert(history.Entry{
			Key: history.EntryKey{
				Title:     "Sousou no Frieren",
				Mode:      "anime",
				MediaType: "anime",
				Season:    1,
				Episode:   ep,
			},
			Title:           "Sousou no Frieren",
			Season:          1,
			Episode:         ep,
			PercentComplete: 1.0,
			Complete:        true,
			AniListID:       154587,
		})
	}

	// 2. Solo Leveling: user completed up to ep 11
	_ = store.Upsert(history.Entry{
		Key: history.EntryKey{
			Title:     "Solo Leveling",
			Mode:      "anime",
			MediaType: "anime",
			Season:    1,
			Episode:   11,
		},
		Title:           "Solo Leveling",
		Season:          1,
		Episode:         11,
		PercentComplete: 1.0,
		Complete:        true,
		AniListID:       151807,
	})

	// 3. Bleach: user already watched up to ep 13
	_ = store.Upsert(history.Entry{
		Key: history.EntryKey{
			Title:     "Bleach: Thousand-Year Blood War",
			Mode:      "anime",
			MediaType: "anime",
			Season:    1,
			Episode:   13,
		},
		Title:           "Bleach: Thousand-Year Blood War",
		Season:          1,
		Episode:         13,
		PercentComplete: 1.0,
		Complete:        true,
	})

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	todayAir := time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)

	releases := []Release{
		{
			ID:        "frieren-8",
			Title:     "Frieren: Beyond Journey's End",
			Mode:      provider.ModeAnime,
			Episode:   8,
			AniListID: 154587,
			AiringAt:  todayAir,
		},
		{
			ID:        "solo-12",
			Title:     "Solo Leveling",
			Mode:      provider.ModeAnime,
			Episode:   12,
			AniListID: 151807,
			AiringAt:  todayAir,
		},
		{
			ID:       "bleach-13",
			Title:    "Bleach: Thousand-Year Blood War",
			Mode:     provider.ModeAnime,
			Episode:  13,
			AiringAt: todayAir,
		},
		{
			ID:       "unknown-1",
			Title:    "Brand New Anime",
			Mode:     provider.ModeAnime,
			Episode:  1,
			AiringAt: todayAir,
		},
	}

	items := CrossReference(releases, store, now)
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	// Frieren: watched 5, airing 8 -> behind by 3 (6, 7, 8)
	if !items[0].InHistory {
		t.Errorf("expected Frieren to be in history")
	}
	if items[0].Status != StatusBehind {
		t.Errorf("Frieren status = %q, want %q", items[0].Status, StatusBehind)
	}
	if items[0].UnwatchedCount != 3 {
		t.Errorf("Frieren unwatched = %d, want 3", items[0].UnwatchedCount)
	}
	if items[0].NextEpisode != 6 {
		t.Errorf("Frieren next ep = %d, want 6", items[0].NextEpisode)
	}
	if items[0].Badge != "Left: Ep 6-8 (3 left)" {
		t.Errorf("Frieren badge = %q, want 'Left: Ep 6-8 (3 left)'", items[0].Badge)
	}

	// Solo Leveling: watched 11, airing 12 today -> new today
	if !items[1].InHistory {
		t.Errorf("expected Solo Leveling to be in history")
	}
	if items[1].Status != StatusNewToday {
		t.Errorf("Solo Leveling status = %q, want %q", items[1].Status, StatusNewToday)
	}
	if items[1].UnwatchedCount != 1 {
		t.Errorf("Solo Leveling unwatched = %d, want 1", items[1].UnwatchedCount)
	}
	if items[1].NextEpisode != 12 {
		t.Errorf("Solo Leveling next ep = %d, want 12", items[1].NextEpisode)
	}
	if items[1].Badge != "▶ Ep 12 (new today)" {
		t.Errorf("Solo Leveling badge = %q, want '▶ Ep 12 (new today)'", items[1].Badge)
	}

	// Bleach: watched 13, airing 13 -> up to date
	if items[2].Status != StatusUpToDate {
		t.Errorf("Bleach status = %q, want %q", items[2].Status, StatusUpToDate)
	}
	if items[2].Badge != "✓ Ep 13 watched" {
		t.Errorf("Bleach badge = %q, want '✓ Ep 13 watched'", items[2].Badge)
	}

	// Brand New Anime: not in history
	if items[3].InHistory {
		t.Errorf("expected Brand New Anime not to be in history")
	}
	if items[3].Status != StatusNotStarted {
		t.Errorf("Brand New Anime status = %q, want %q", items[3].Status, StatusNotStarted)
	}
	if items[3].Badge != "Ep 1" {
		t.Errorf("Brand New Anime badge = %q, want 'Ep 1'", items[3].Badge)
	}
}

func TestCrossReference_Series(t *testing.T) {
	histPath := filepath.Join(t.TempDir(), "history.json")
	store, err := history.NewStore(histPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Severance S01 complete
	for ep := 1; ep <= 9; ep++ {
		_ = store.Upsert(history.Entry{
			Key: history.EntryKey{
				Title:     "Severance",
				Mode:      "tv",
				MediaType: "tv",
				Season:    1,
				Episode:   ep,
			},
			Title:           "Severance",
			Season:          1,
			Episode:         ep,
			PercentComplete: 1.0,
			Complete:        true,
			TMDBID:          95396,
		})
	}

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	todayAir := time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC)

	releases := []Release{
		{
			ID:       "sev-s2e1",
			Title:    "Severance",
			Mode:     provider.ModeTV,
			Season:   2,
			Episode:  1,
			TMDBID:   95396,
			AiringAt: todayAir,
		},
	}

	items := CrossReference(releases, store, now)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	// Severance: S1 complete, S2 not started yet -> Left S02E01-E01 (1 left)
	if !items[0].InHistory {
		t.Errorf("expected Severance to be in history")
	}
	if items[0].Status != StatusBehind {
		t.Errorf("Severance status = %q, want %q", items[0].Status, StatusBehind)
	}
	if items[0].NextEpisode != 1 {
		t.Errorf("Severance next episode = %d, want 1", items[0].NextEpisode)
	}
}

func TestBuildWeeklySchedule(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) // Thursday
	monday := StartOfWeek(now)

	releases := []Release{
		{
			ID:       "rel-1",
			Title:    "Monday Show",
			Mode:     provider.ModeAnime,
			Episode:  1,
			AiringAt: monday.Add(10 * time.Hour), // Monday 10:00
		},
		{
			ID:       "rel-2",
			Title:    "Thursday Show",
			Mode:     provider.ModeAnime,
			Episode:  5,
			AiringAt: monday.Add(3*24*time.Hour + 14*time.Hour), // Thursday 14:00
		},
	}

	schedule := BuildWeeklySchedule(releases, nil, monday, now)
	if len(schedule.Days) != 7 {
		t.Fatalf("expected 7 days, got %d", len(schedule.Days))
	}

	// Monday is day 0
	if len(schedule.Days[0].Releases) != 1 {
		t.Errorf("Monday releases = %d, want 1", len(schedule.Days[0].Releases))
	}
	if schedule.Days[0].Releases[0].Release.Title != "Monday Show" {
		t.Errorf("Monday release title = %q", schedule.Days[0].Releases[0].Release.Title)
	}

	// Thursday is day 3
	if len(schedule.Days[3].Releases) != 1 {
		t.Errorf("Thursday releases = %d, want 1", len(schedule.Days[3].Releases))
	}
	if !schedule.Days[3].IsToday {
		t.Errorf("expected Thursday to be marked IsToday")
	}
}

func TestClient_FetchAnimeWeek(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"data": map[string]any{
				"Page": map[string]any{
					"pageInfo": map[string]any{
						"hasNextPage": false,
					},
					"airingSchedules": []map[string]any{
						{
							"id":       101,
							"airingAt": 1793871000,
							"episode":  8,
							"media": map[string]any{
								"id":    154587,
								"idMal": 52991,
								"title": map[string]any{
									"english": "Frieren: Beyond Journey's End",
									"romaji":  "Sousou no Frieren",
								},
								"format":          "TV",
								"status":          "RELEASING",
								"episodes":        28,
								"countryOfOrigin": "JP",
								"isAdult":         false,
								"averageScore":    89,
								"coverImage": map[string]any{
									"extraLarge": "https://s4.anilist.co/file/frieren.png",
								},
							},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewClientWithEndpoints(ts.Client(), nil, ts.URL, "", "", "")
	start := time.Unix(1793800000, 0)
	end := start.Add(7 * 24 * time.Hour)

	releases, err := client.FetchAnimeWeek(context.Background(), start, end)
	if err != nil {
		t.Fatalf("FetchAnimeWeek failed: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(releases))
	}
	if releases[0].Title != "Frieren: Beyond Journey's End" {
		t.Errorf("title = %q, want Frieren", releases[0].Title)
	}
	if releases[0].Episode != 8 {
		t.Errorf("episode = %d, want 8", releases[0].Episode)
	}
	if releases[0].AniListID != 154587 {
		t.Errorf("anilistID = %d, want 154587", releases[0].AniListID)
	}
}

func TestClient_FetchSeriesWeek_Trakt(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]any{
			{
				"first_aired": "2026-11-05T01:00:00.000Z",
				"episode": map[string]any{
					"season": 12,
					"number": 5,
					"title":  "Baptism by Fire",
				},
				"show": map[string]any{
					"title": "Chicago Med",
					"year":  2015,
					"ids": map[string]any{
						"trakt": 1234,
						"tmdb":  63639,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer ts.Close()

	client := NewClientWithEndpoints(ts.Client(), nil, "", ts.URL, "test-client-id", "")
	releases, err := client.FetchSeriesWeek(context.Background(), time.Now(), 7)
	if err != nil {
		t.Fatalf("FetchSeriesWeek failed: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(releases))
	}
	if releases[0].Title != "Chicago Med" {
		t.Errorf("title = %q, want Chicago Med", releases[0].Title)
	}
	if releases[0].Season != 12 || releases[0].Episode != 5 {
		t.Errorf("season/episode = S%02dE%02d, want S12E05", releases[0].Season, releases[0].Episode)
	}
}

func TestClient_FetchSeriesWeek_TMDBFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"results": []map[string]any{
				{
					"id":             31991,
					"name":           "WWE NXT",
					"overview":       "Pro wrestling",
					"first_air_date": "2010-02-23",
					"vote_average":   7.8,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	pool := tmdb.NewKeyPool([]string{"dummy-key"})
	// Trakt endpoint points to dead URL to trigger TMDB fallback
	client := NewClientWithEndpoints(ts.Client(), pool, "", "http://127.0.0.1:99999", "", ts.URL)
	releases, err := client.FetchSeriesWeek(context.Background(), time.Now(), 7)
	if err != nil {
		t.Fatalf("FetchSeriesWeek fallback failed: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected 1 release from fallback, got %d", len(releases))
	}
	if releases[0].Title != "WWE NXT" {
		t.Errorf("title = %q, want WWE NXT", releases[0].Title)
	}
}

func TestLiveSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := NewClient(nil)
	now := time.Now()
	weekStart := StartOfWeek(now)

	releases, err := client.FetchWeekReleases(ctx, weekStart, true)
	if err != nil {
		t.Logf("live fetch notice: %v", err)
		return
	}
	t.Logf("fetched %d live releases", len(releases))
}
