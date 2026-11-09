package anidream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kari/internal/provider"
)

// serve wraps a handler at one server-function hash.
func serve(t *testing.T, hash string, result any) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/_serverFn/"+hash, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tsr-Serverfn") == "" {
			t.Errorf("missing x-tsr-serverFn header")
		}
		if r.Header.Get("Referer") == "" || r.Header.Get("Origin") == "" {
			t.Errorf("missing origin/referer headers")
		}
		if _, err := decodeServerFnPayload(r.URL.Query().Get("payload")); err != nil {
			t.Errorf("payload did not decode: %v", err)
		}
		body, err := encodeServerFnResponse(result)
		if err != nil {
			t.Fatalf("encode response: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := NewClientWithBase(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithBase: %v", err)
	}
	return c
}

// decodeServerFnPayload verifies the request envelope and returns its data.
func decodeServerFnPayload(payload string) (map[string]any, error) {
	var wrapper map[string]any
	if err := json.Unmarshal([]byte(payload), &wrapper); err != nil {
		return nil, err
	}
	if wrapper["f"] == nil || wrapper["m"] == nil {
		return nil, errors.New("missing wrapper fields")
	}
	decoder := &serovalDecoder{refs: map[int]any{}}
	value, err := decoder.decode(wrapper["t"])
	if err != nil {
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("payload root is not an object")
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		return nil, errors.New("payload has no data object")
	}
	return data, nil
}

func TestSerovalRoundTrip(t *testing.T) {
	encoded, err := marshalServerFnPayload(map[string]any{
		"data": map[string]any{
			"id":    21,
			"dub":   false,
			"query": "one piece",
			"tags":  []any{"action", "adventure"},
			"none":  nil,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wrapper map[string]any
	if err := json.Unmarshal(encoded, &wrapper); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	decoder := &serovalDecoder{refs: map[int]any{}}
	value, err := decoder.decode(wrapper["t"])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	data := value.(map[string]any)["data"].(map[string]any)
	if data["query"] != "one piece" {
		t.Errorf("query = %v", data["query"])
	}
	if data["id"] != float64(21) {
		t.Errorf("id = %v (%T)", data["id"], data["id"])
	}
	if data["dub"] != false {
		t.Errorf("dub = %v", data["dub"])
	}
	if tags := data["tags"].([]any); len(tags) != 2 || tags[0] != "action" {
		t.Errorf("tags = %v", tags)
	}
	if data["none"] != nil {
		t.Errorf("none = %v", data["none"])
	}
}

func TestDecodeServerFnEnvelope(t *testing.T) {
	raw := `{"t":10,"i":0,"p":{"k":["result","error","context"],"v":[{"t":9,"i":1,"a":[{"t":10,"i":2,"p":{"k":["number","has_sub"],"v":[{"t":0,"s":1},{"t":2,"s":2}]},"o":0}]},{"t":2,"s":1},{"t":11,"i":3,"p":{"k":[],"v":[]},"o":0}]},"o":0}`
	result, err := decodeServerFnResponse([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	items := result.([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	first := items[0].(map[string]any)
	if first["number"] != float64(1) || first["has_sub"] != true {
		t.Errorf("first = %v", first)
	}
}

func TestDecodeServerFnError(t *testing.T) {
	raw := `{"t":10,"i":0,"p":{"k":["result","error","context"],"v":[{"t":2,"s":1},{"t":25,"i":1,"s":{"message":{"t":1,"s":"upstream exploded"}},"c":"$TSR/Error"},{"t":11,"i":2,"p":{"k":[],"v":[]},"o":0}]},"o":0}`
	_, err := decodeServerFnResponse([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("err = %v", err)
	}
}

func TestSearch(t *testing.T) {
	response := map[string]any{
		"items": []any{
			map[string]any{
				"id":          21,
				"mal_id":      21,
				"format":      "TV",
				"title":       map[string]any{"english": "One Piece", "romaji": "ONE PIECE"},
				"start_date":  map[string]any{"year": 1999},
				"cover_image": map[string]any{"large": "https://img.example/op.jpg"},
				"description": "A pirate \\x3Cbr> story",
				"genres":      []any{"Action", "Adventure"},
			},
			map[string]any{
				"id":         936,
				"format":     "MOVIE",
				"title":      map[string]any{"romaji": "Naruto the Movie"},
				"start_date": map[string]any{"year": 2004},
			},
		},
	}
	c := serve(t, fnBrowse, response)

	results, err := c.Search(context.Background(), "one piece", provider.ModeAnime)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	first := results[0]
	if first.Title != "One Piece" || first.ID != "21" || first.AniListID != 21 || first.MALID != 21 {
		t.Errorf("first = %+v", first)
	}
	if first.Type != provider.ModeAnime || first.MediaType != provider.MediaTypeAnime {
		t.Errorf("typing = %+v", first)
	}
	if first.Year != "1999" || first.CoverURL != "https://img.example/op.jpg" {
		t.Errorf("metadata = %+v", first)
	}
	if strings.Contains(first.Overview, "<") || !strings.Contains(first.Overview, "A pirate") {
		t.Errorf("overview = %q", first.Overview)
	}
	if results[1].MediaType != provider.MediaTypeMovie {
		t.Errorf("movie media type = %q", results[1].MediaType)
	}
}

func TestSearchManga(t *testing.T) {
	response := map[string]any{
		"items": []any{
			map[string]any{
				"id":     85737,
				"format": "NOVEL",
				"title":  map[string]any{"english": "Re:ZERO"},
			},
			map[string]any{
				"id":     85738,
				"format": "MANGA",
				"title":  map[string]any{"english": "Re:ZERO comic"},
			},
			map[string]any{
				"id":     85739,
				"format": "COMIC",
				"title":  map[string]any{"english": "Graphic Story"},
			},
		},
	}
	c := serve(t, fnBrowse, response)
	results, err := c.Search(context.Background(), "rezero", provider.ModeManga)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results[0].Type != provider.ModeManga || results[0].MediaType != provider.MediaTypeNovel {
		t.Errorf("typing = %+v", results[0])
	}
	if results[1].MediaType != provider.MediaTypeManga || results[2].MediaType != provider.MediaTypeComic {
		t.Errorf("manga/comic media types = %q/%q", results[1].MediaType, results[2].MediaType)
	}
}

func TestFetchEpisodes(t *testing.T) {
	response := []any{
		map[string]any{
			"number":    1,
			"title":     map[string]any{"english": "Enter Luffy"},
			"has_sub":   true,
			"has_dub":   true,
			"is_filler": false,
		},
		map[string]any{
			"number":  2.5,
			"title":   map[string]any{"romaji": "Special"},
			"has_sub": true,
		},
	}
	c := serve(t, fnEpisodes, response)
	episodes, err := c.FetchEpisodes(context.Background(), provider.SearchResult{ID: "21", Title: "One Piece"})
	if err != nil {
		t.Fatalf("FetchEpisodes: %v", err)
	}
	if len(episodes) != 2 {
		t.Fatalf("episodes = %d, want 2 (fractional row skipped)", len(episodes))
	}
	if episodes[0].Audio != provider.AudioSub || episodes[1].Audio != provider.AudioDub {
		t.Errorf("audio = %q, %q", episodes[0].Audio, episodes[1].Audio)
	}
	if episodes[0].Episode != 1 || episodes[0].Title != "Enter Luffy" {
		t.Errorf("episode = %+v", episodes[0])
	}
	if episodes[0].ID != "watch/anidream/21/sub/1" {
		t.Errorf("id = %q", episodes[0].ID)
	}
}

func TestResolveSource(t *testing.T) {
	response := map[string]any{
		"ok": true,
		"data": map[string]any{
			"streamUrl": "https://cdn.example/master.m3u8?token=abc",
			"audios":    []any{"dual"},
			"subtitles": []any{
				map[string]any{"label": "English", "url": "https://cdn.example/en.ass", "language": "English", "default": true},
				map[string]any{"label": "Duplicate", "url": "https://cdn.example/en.ass", "language": "English"},
			},
			"chapters": []any{
				map[string]any{"title": "Opening", "start": 30, "end": 111, "type": "intro"},
				map[string]any{"title": "Ending", "start": 1372, "end": 1445, "type": "outro"},
			},
		},
	}
	c := serve(t, fnPlayback, response)
	sources, err := c.ResolveSource(context.Background(), "21", provider.Episode{
		ID:      episodeHandle(21, provider.AudioSub, 1),
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if err != nil {
		t.Fatalf("ResolveSource: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d", len(sources))
	}
	source := sources[0]
	if source.URL != "https://cdn.example/master.m3u8?token=abc" {
		t.Errorf("url = %q", source.URL)
	}
	if source.Type != provider.SourceTypeHLS || source.SubType != provider.SubTypeSoft {
		t.Errorf("typing = %+v", source)
	}
	if len(source.Subtitles) != 1 || source.Subtitles[0].Language != "en" || !source.Subtitles[0].Default {
		t.Errorf("subtitles = %+v", source.Subtitles)
	}
	if source.SkipTimes == nil || source.SkipTimes.OpStart != 30 || source.SkipTimes.EdEnd != 1445 {
		t.Errorf("skip times = %+v", source.SkipTimes)
	}
}

func TestResolveSourceDubUnavailable(t *testing.T) {
	response := map[string]any{
		"ok": true,
		"data": map[string]any{
			"streamUrl": "https://cdn.example/master.m3u8",
			"audios":    []any{"sub"},
		},
	}
	c := serve(t, fnPlayback, response)
	_, err := c.ResolveSource(context.Background(), "21", provider.Episode{
		ID:      episodeHandle(21, provider.AudioDub, 1),
		Episode: 1,
		Audio:   provider.AudioDub,
	})
	if !errors.Is(err, provider.ErrAudioUnavailable) {
		t.Fatalf("err = %v, want ErrAudioUnavailable", err)
	}
}

func TestResolveSourceNotFound(t *testing.T) {
	response := map[string]any{
		"ok":      false,
		"code":    "NOT_FOUND",
		"message": "Episode 1 isn't available yet",
	}
	c := serve(t, fnPlayback, response)
	_, err := c.ResolveSource(context.Background(), "21", provider.Episode{
		ID:      episodeHandle(21, provider.AudioSub, 1),
		Episode: 1,
		Audio:   provider.AudioSub,
	})
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFetchChaptersCachesListing(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/_serverFn/"+fnChapters, func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := encodeServerFnResponse([]any{map[string]any{"slug": "chapter-1", "number": 1, "name": "Chapter 1"}})
		if err != nil {
			t.Errorf("encode response: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := NewClientWithBase(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithBase: %v", err)
	}

	first, err := c.FetchChapters(context.Background(), provider.SearchResult{ID: "94970"})
	if err != nil {
		t.Fatalf("FetchChapters: %v", err)
	}
	if len(first) != 1 || first[0].ID != "94970|chapter-1" {
		t.Fatalf("chapters = %+v", first)
	}
	second, err := c.FetchChapters(context.Background(), provider.SearchResult{ID: "94970"})
	if err != nil {
		t.Fatalf("FetchChapters cached: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("cached chapters = %+v", second)
	}
	if calls != 1 {
		t.Errorf("server function calls = %d, want 1 (second lookup served from cache)", calls)
	}

	// A different series must not be served from the first series' entry.
	other, err := c.FetchChapters(context.Background(), provider.SearchResult{ID: "30013"})
	if err != nil {
		t.Fatalf("FetchChapters uncached series: %v", err)
	}
	if len(other) != 1 || other[0].ID != "30013|chapter-1" {
		t.Fatalf("uncached series chapters = %+v", other)
	}
	if calls != 2 {
		t.Errorf("server function calls = %d, want 2 (per-series cache misses must refetch)", calls)
	}
}

func TestFetchChaptersCoalescesConcurrentRequests(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/_serverFn/"+fnChapters, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		body, err := encodeServerFnResponse([]any{map[string]any{"slug": "chapter-1", "number": 1}})
		if err != nil {
			t.Errorf("encode response: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := NewClientWithBase(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithBase: %v", err)
	}

	const callers = 6
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := c.FetchChapters(context.Background(), provider.SearchResult{ID: "94970"}); err != nil {
				t.Errorf("FetchChapters: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("server function calls = %d, want 1 for concurrent requests", got)
	}
}

func TestMangaFlow(t *testing.T) {
	chapters := []any{
		map[string]any{"slug": "chapter-10", "number": 10, "name": "Chapter 10: The End"},
		map[string]any{"slug": "chapter-8-1", "number": 8.1, "name": "Chapter 8.1"},
	}
	c := serve(t, fnChapters, chapters)

	list, err := c.FetchChapters(context.Background(), provider.SearchResult{ID: "30013"})
	if err != nil {
		t.Fatalf("FetchChapters: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("chapters = %d", len(list))
	}
	if list[0].Number != "8.1" || list[1].Number != "10" {
		t.Errorf("order/numbers = %q, %q", list[0].Number, list[1].Number)
	}
	if list[0].ID != "30013|chapter-8-1" || list[0].Language != "en" {
		t.Errorf("chapter = %+v", list[0])
	}
	if list[0].Title != "" || list[1].Title != "The End" {
		t.Errorf("titles = %q, %q", list[0].Title, list[1].Title)
	}

	pagesClient := serve(t, fnChapterPages, []any{
		map[string]any{"url": "https://img.example/2.jpg", "index": 1},
		map[string]any{"url": "https://img.example/1.jpg", "index": 0},
		map[string]any{"url": "https://img.example/1.jpg", "index": 0},
	})
	pages, err := pagesClient.FetchPages(context.Background(), list[0])
	if err != nil {
		t.Fatalf("FetchPages: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2 after dedupe", len(pages))
	}
	if pages[0].URL != "https://img.example/1.jpg" || pages[1].URL != "https://img.example/2.jpg" {
		t.Errorf("page order = %+v", pages)
	}
}
