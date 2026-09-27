package scrobble

// Thorough pins for one-way tracker import. These run against httptest
// fakes speaking the real AniList GraphQL and Trakt REST shapes, so a
// green suite means the fetchers handle the documented API contracts.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func anilistImportServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body strings.Builder
		_, _ = body.WriteString("")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		payload := string(buf)

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(payload, "Viewer"):
			_, _ = w.Write([]byte(`{"data":{"Viewer":{"id":123}}}`))
		case strings.Contains(payload, "MediaListCollection"):
			_, _ = w.Write([]byte(`{"data":{"MediaListCollection":{"lists":[
				{"entries":[
					{"status":"CURRENT","progress":3,"updatedAt":1700000000,
					 "media":{"id":154587,"title":{"english":"Frieren: Beyond Journey's End","romaji":"Sousou no Frieren"},"episodes":28,"format":"TV"}},
					{"status":"COMPLETED","progress":24,"updatedAt":1700000100,
					 "media":{"title":{"english":"Full Complete","romaji":""},"episodes":24,"format":"TV"}},
					{"status":"COMPLETED","progress":1,"updatedAt":1700000200,
					 "media":{"title":{"english":"A Movie","romaji":""},"episodes":1,"format":"MOVIE"}},
					{"status":"CURRENT","progress":2,"updatedAt":1700000300,
					 "media":{"title":{"english":"","romaji":"Romaji Only"},"episodes":12,"format":"TV"}},
					{"status":"PLANNING","progress":0,"updatedAt":1700000400,
					 "media":{"title":{"english":"Not Started","romaji":""},"episodes":12,"format":"TV"}},
					{"status":"DROPPED","progress":5,"updatedAt":1700000500,
					 "media":{"title":{"english":"Dropped Show","romaji":""},"episodes":12,"format":"TV"}},
					{"status":"CURRENT","progress":0,"updatedAt":1700000600,
					 "media":{"title":{"english":"Zero Progress","romaji":""},"episodes":12,"format":"TV"}},
					{"status":"COMPLETED","progress":0,"updatedAt":1700000700,
					 "media":{"title":{"english":"Contradiction","romaji":""},"episodes":null,"format":"TV"}},
					{"status":"CURRENT","progress":4,"updatedAt":1700000800,
					 "media":{"title":{"english":"","romaji":""},"episodes":12,"format":"TV"}}
				]}
			]}}}`))
		default:
			t.Errorf("unexpected AniList query: %s", payload)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
}

func testAnilistClient(ts *httptest.Server) *AniListClient {
	return &AniListClient{
		httpClient: ts.Client(),
		apiBase:    ts.URL,
		token:      &AniListToken{AccessToken: "test"},
	}
}

func TestFetchWatchedListExpandsRanges(t *testing.T) {
	ts := anilistImportServer(t)
	defer ts.Close()

	items, err := testAnilistClient(ts).FetchWatchedList(context.Background())
	if err != nil {
		t.Fatalf("FetchWatchedList failed: %v", err)
	}

	// 3 (frieren) + 24 (complete) + 1 (movie) + 2 (romaji) = 30.
	if len(items) != 30 {
		t.Fatalf("items = %d, want 30", len(items))
	}

	// Frieren: episodes 1-2 complete, 3 in progress.
	frieren := items[:3]
	for i, it := range frieren {
		if it.Title != "Frieren: Beyond Journey's End" {
			t.Fatalf("item %d title = %q", i, it.Title)
		}
		if it.Mode != "anime" || it.MediaType != "anime" || it.Season != 1 || it.Episode != i+1 {
			t.Errorf("item %d = %+v, want anime/anime s1e%d", i, it, i+1)
		}
	}
	if !frieren[0].Complete || !frieren[1].Complete {
		t.Error("episodes before progress must be complete")
	}
	if frieren[2].Complete {
		t.Error("progress episode of CURRENT must stay in-progress")
	}
	if got := frieren[0].WatchedAt.Unix(); got != 1700000000 {
		t.Errorf("WatchedAt = %d, want updatedAt 1700000000", got)
	}
	if frieren[0].AniListID != 154587 {
		t.Errorf("AniListID = %d, want 154587", frieren[0].AniListID)
	}
}

func TestFetchWatchedListSkipsUnwatched(t *testing.T) {
	ts := anilistImportServer(t)
	defer ts.Close()

	items, err := testAnilistClient(ts).FetchWatchedList(context.Background())
	if err != nil {
		t.Fatalf("FetchWatchedList failed: %v", err)
	}
	for _, it := range items {
		switch it.Title {
		case "Not Started", "Dropped Show", "Zero Progress", "Contradiction", "":
			t.Errorf("must skip %q", it.Title)
		}
	}
}

func TestFetchWatchedListMovieAndRomaji(t *testing.T) {
	ts := anilistImportServer(t)
	defer ts.Close()

	items, err := testAnilistClient(ts).FetchWatchedList(context.Background())
	if err != nil {
		t.Fatalf("FetchWatchedList failed: %v", err)
	}
	byTitle := map[string]WatchedItem{}
	for _, it := range items {
		if _, ok := byTitle[it.Title]; !ok {
			byTitle[it.Title] = it
		}
	}
	movie, ok := byTitle["A Movie"]
	if !ok {
		t.Fatal("movie entry missing")
	}
	if movie.MediaType != "movie" || !movie.Complete {
		t.Errorf("movie = %+v, want movie/complete", movie)
	}
	romaji, ok := byTitle["Romaji Only"]
	if !ok {
		t.Fatal("romaji-fallback entry missing")
	}
	if romaji.Episode != 1 || romaji.Mode != "anime" {
		t.Errorf("romaji = %+v", romaji)
	}
}

func TestFetchWatchedListRequiresAuth(t *testing.T) {
	c := &AniListClient{}
	if _, err := c.FetchWatchedList(context.Background()); err == nil {
		t.Error("expected an error when not connected")
	}
}

func TestFetchWatchedListServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer ts.Close()

	if _, err := testAnilistClient(ts).FetchWatchedList(context.Background()); err == nil {
		t.Error("expected an error on 401")
	}
}

func TestFetchWatchedListBadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(buf), "Viewer") {
			_, _ = w.Write([]byte(`{"data":{"Viewer":{"id":123}}}`))
			return
		}
		_, _ = w.Write([]byte(`this is not json`))
	}))
	defer ts.Close()

	// Viewer resolves, collection decode fails.
	if _, err := testAnilistClient(ts).FetchWatchedList(context.Background()); err == nil {
		t.Error("expected a decode error")
	}
}

func traktImportServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("trakt-api-version") != "2" {
			t.Errorf("missing trakt-api-version header")
		}
		if r.Header.Get("Authorization") == "" {
			t.Errorf("missing Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		switch page := r.URL.Query().Get("page"); page {
		case "", "1":
			w.Header().Set("X-Pagination-Page-Count", "2")
			w.Header().Set("X-Pagination-Limit", "5")
			if got := r.URL.Query().Get("limit"); got != "250" {
				t.Errorf("limit = %q, want 250 (API max page size)", got)
			}
			_, _ = w.Write([]byte(`[
				{"type":"episode","watched_at":"2024-05-01T10:00:00.000Z",
				 "show":{"title":"The Boys","ids":{"tmdb":123}},
				 "episode":{"season":1,"number":1}},
				{"type":"movie","watched_at":"2024-05-02T10:00:00.000Z",
				 "movie":{"title":"Dune","ids":{"tmdb":456}}},
				{"type":"person","watched_at":"2024-05-03T10:00:00.000Z"},
				{"type":"episode","watched_at":"2024-05-04T10:00:00.000Z",
				 "show":{"title":"The Boys","ids":{"tmdb":123}},
				 "episode":{"season":1,"number":0}},
				{"type":"episode","watched_at":"2024-05-05T10:00:00.000Z",
				 "show":{"title":"","ids":{"tmdb":123}},
				 "episode":{"season":1,"number":2}}
			]`))
		case "2":
			w.Header().Set("X-Pagination-Page-Count", "2")
			w.Header().Set("X-Pagination-Limit", "5")
			_, _ = w.Write([]byte(`[
				{"type":"episode","watched_at":"2024-05-06T10:00:00.000Z",
				 "show":{"title":"The Boys","ids":{"tmdb":123}},
				 "episode":{"season":2,"number":1}}
			]`))
		default:
			t.Errorf("unexpected page %q", page)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
}

func testTraktClient(ts *httptest.Server) *TraktClient {
	return &TraktClient{
		httpClient: ts.Client(),
		apiBase:    ts.URL,
		token:      &TraktClient_Token,
	}
}

var TraktClient_Token = TraktToken{
	AccessToken: "test",
	ExpiresAt:   time.Now().Add(time.Hour),
}

func TestFetchWatchedHistoryPages(t *testing.T) {
	ts := traktImportServer(t)
	defer ts.Close()

	items, err := testTraktClient(ts).FetchWatchedHistory(context.Background())
	if err != nil {
		t.Fatalf("FetchWatchedHistory failed: %v", err)
	}
	// S01E01 + movie + S02E01; person/zero-number/empty-title skipped.
	if len(items) != 3 {
		t.Fatalf("items = %+v, want 3", items)
	}

	ep := items[0]
	if ep.Title != "The Boys" || ep.Mode != "tv" || ep.MediaType != "tv" {
		t.Errorf("ep = %+v", ep)
	}
	if ep.Season != 1 || ep.Episode != 1 || ep.TMDBID != 123 || !ep.Complete {
		t.Errorf("ep = %+v", ep)
	}
	if ep.WatchedAt.Year() != 2024 || ep.WatchedAt.Month() != 5 || ep.WatchedAt.Day() != 1 {
		t.Errorf("ep.WatchedAt = %v", ep.WatchedAt)
	}

	var movie *WatchedItem
	for i := range items {
		if items[i].MediaType == "movie" {
			movie = &items[i]
		}
	}
	if movie == nil {
		t.Fatal("movie entry missing")
	}
	if movie.Title != "Dune" || movie.Mode != "movies" || movie.TMDBID != 456 || !movie.Complete {
		t.Errorf("movie = %+v", movie)
	}
}

func TestFetchWatchedListFollowsChunks(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		payload := string(buf)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(payload, "Viewer") {
			_, _ = w.Write([]byte(`{"data":{"Viewer":{"id":7}}}`))
			return
		}
		if !strings.Contains(payload, "MediaListCollection") {
			t.Errorf("unexpected AniList query: %s", payload)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// First chunk announces another; the second closes the list.
		// Chunk variables must travel with the query or big lists
		// silently truncate after the first chunk.
		if strings.Contains(payload, `"chunk":2`) {
			_, _ = w.Write([]byte(`{"data":{"MediaListCollection":{"hasNextChunk":false,"lists":[
				{"entries":[
					{"status":"CURRENT","progress":1,"updatedAt":1700000900,
					 "media":{"title":{"english":"Chunk Two","romaji":""},"episodes":12,"format":"TV"}}
				]}
			]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"MediaListCollection":{"hasNextChunk":true,"lists":[
			{"entries":[
				{"status":"COMPLETED","progress":2,"updatedAt":1700000800,
				 "media":{"title":{"english":"Chunk One","romaji":""},"episodes":2,"format":"TV"}}
			]}
		]}}}`))
	}))
	defer ts.Close()

	items, err := testAnilistClient(ts).FetchWatchedList(context.Background())
	if err != nil {
		t.Fatalf("FetchWatchedList failed: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %+v, want 2 from chunk one + 1 from chunk two", items)
	}
	titles := map[string]bool{}
	for _, it := range items {
		titles[it.Title] = true
	}
	if !titles["Chunk One"] || !titles["Chunk Two"] {
		t.Errorf("missing chunk entries: %v", titles)
	}
}

func TestFetchWatchedHistoryShortPageStops(t *testing.T) {
	var requests int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Pagination-Page-Count", "4")
		w.Header().Set("X-Pagination-Limit", "5")
		// Fewer raw records than the page holds: last page even
		// though the page count advertises more.
		_, _ = w.Write([]byte(`[
			{"type":"movie","watched_at":"2024-05-02T10:00:00.000Z",
			 "movie":{"title":"Dune","ids":{"tmdb":456}}}
		]`))
	}))
	defer ts.Close()

	items, err := testTraktClient(ts).FetchWatchedHistory(context.Background())
	if err != nil {
		t.Fatalf("FetchWatchedHistory failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want 1", items)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1 (short page stops paging)", requests)
	}
}

func TestFetchWatchedHistoryRequiresAuth(t *testing.T) {
	c := &TraktClient{}
	if _, err := c.FetchWatchedHistory(context.Background()); err == nil {
		t.Error("expected an error when not connected")
	}
}

func TestFetchWatchedHistoryServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer ts.Close()

	if _, err := testTraktClient(ts).FetchWatchedHistory(context.Background()); err == nil {
		t.Error("expected an error on 403")
	}
}

func TestFetchWatchedHistoryBadTimestampKept(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"type":"movie","watched_at":"not-a-time",
			 "movie":{"title":"Dune","ids":{"tmdb":456}}}
		]`))
	}))
	defer ts.Close()

	items, err := testTraktClient(ts).FetchWatchedHistory(context.Background())
	if err != nil {
		t.Fatalf("FetchWatchedHistory failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("bad timestamp must not lose the watch, got %+v", items)
	}
	if time.Since(items[0].WatchedAt) > time.Minute {
		t.Errorf("fallback WatchedAt = %v, want ~now", items[0].WatchedAt)
	}
}

func mangaImportServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		payload := string(buf)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(payload, "Viewer") {
			_, _ = w.Write([]byte(`{"data":{"Viewer":{"id":9}}}`))
			return
		}
		if !strings.Contains(payload, "MANGA") {
			t.Errorf("manga list must query type MANGA: %s", payload)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"MediaListCollection":{"hasNextChunk":false,"lists":[
			{"entries":[
				{"status":"CURRENT","progress":12,"updatedAt":1700001000,
				 "media":{"id":21,"title":{"english":"One Piece","romaji":""},"episodes":null,"format":"MANGA"}},
				{"status":"COMPLETED","progress":50,"updatedAt":1700001100,
				 "media":{"title":{"english":"","romaji":"Berserk"},"episodes":null,"format":"MANGA"}},
				{"status":"CURRENT","progress":0,"updatedAt":1700001200,
				 "media":{"title":{"english":"Zero Manga","romaji":""},"episodes":null,"format":"MANGA"}},
				{"status":"DROPPED","progress":7,"updatedAt":1700001300,
				 "media":{"title":{"english":"Dropped Manga","romaji":""},"episodes":null,"format":"MANGA"}}
			]}
		]}}}`))
	}))
}

func TestFetchMangaListPositions(t *testing.T) {
	ts := mangaImportServer(t)
	defer ts.Close()

	items, err := testAnilistClient(ts).FetchMangaList(context.Background())
	if err != nil {
		t.Fatalf("FetchMangaList failed: %v", err)
	}
	// One position per title: current ch12 + completed ch50.
	if len(items) != 2 {
		t.Fatalf("items = %+v, want 2", items)
	}
	cur := items[0]
	if cur.Title != "One Piece" || cur.Mode != "manga" || cur.MediaType != "manga" {
		t.Errorf("current = %+v", cur)
	}
	if cur.Chapter != "12" || cur.Complete {
		t.Errorf("current = %+v, want chapter 12 in-progress", cur)
	}
	if cur.AniListID != 21 {
		t.Errorf("AniListID = %d, want 21", cur.AniListID)
	}
	done := items[1]
	if done.Title != "Berserk" || done.Chapter != "50" || !done.Complete {
		t.Errorf("completed = %+v, want chapter 50 complete", done)
	}
}

func TestFetchMangaListRequiresAuth(t *testing.T) {
	c := &AniListClient{}
	if _, err := c.FetchMangaList(context.Background()); err == nil {
		t.Error("expected an error when not connected")
	}
}

func TestFetchWatchedHistoryProceedsOnRefreshFailure(t *testing.T) {
	// A dead refresh endpoint must not block the fetch: the current
	// access token may still be honored.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/oauth/token") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"type":"movie","watched_at":"2024-05-02T10:00:00.000Z",
			 "movie":{"title":"Dune","ids":{"tmdb":456}}}
		]`))
	}))
	defer ts.Close()

	c := &TraktClient{
		httpClient: ts.Client(),
		apiBase:    ts.URL,
		token: &TraktToken{
			AccessToken:  "still-valid",
			RefreshToken: "dead",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	items, err := c.FetchWatchedHistory(context.Background())
	if err != nil {
		t.Fatalf("refresh failure must not abort the fetch: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Dune" {
		t.Fatalf("items = %+v, want Dune", items)
	}
}
