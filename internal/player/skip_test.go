package player

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"kari/internal/animeskip"
	"kari/internal/aniskip"
	"kari/internal/introdb"
	"kari/internal/model"
	"kari/internal/skipdb"
	"kari/internal/util"
)

func newTestSkipCache() *util.BoundedCache[combinedSkipTimes] {
	return util.NewBoundedCache[combinedSkipTimes](100)
}

func TestGetSkipArgs_OffProvider(t *testing.T) {
	args, scriptPath := getSkipArgs(SkipClients{}, SkipSettings{Provider: "off"}, model.ResolvedMedia{
		SeriesTitle:   "Frieren",
		EpisodeNumber: 1,
	}, newTestSkipCache())
	if args != nil || scriptPath != "" {
		t.Fatalf("expected nil args when provider is off, got args=%v path=%s", args, scriptPath)
	}
}

func TestGetSkipArgs_EmptyTitle(t *testing.T) {
	args, scriptPath := getSkipArgs(SkipClients{}, SkipSettings{Provider: "hybrid"}, model.ResolvedMedia{
		SeriesTitle:   "",
		EpisodeNumber: 1,
	}, newTestSkipCache())
	if args != nil || scriptPath != "" {
		t.Fatalf("expected nil args for empty title, got args=%v path=%s", args, scriptPath)
	}
}

func TestBuildSkipArgsFromTimes(t *testing.T) {
	times := combinedSkipTimes{
		OpStart:      45.0,
		OpEnd:        135.0,
		EdStart:      1350.0,
		EdEnd:        1440.0,
		RecapStart:   0.0,
		RecapEnd:     45.0,
		PreviewStart: 1440.0,
		PreviewEnd:   1470.0,
	}
	settings := SkipSettings{
		Provider:       "hybrid",
		AutoSkipIntro:  true,
		AutoSkipEnding: true,
		SkipRecap:      true,
		SkipPreview:    true,
	}

	args, scriptPath := buildSkipArgsFromTimes(times, settings)
	defer cleanupSkipScript(scriptPath)

	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if scriptPath == "" {
		t.Fatal("expected non-empty scriptPath")
	}

	hasScript := false
	hasScriptOpts := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "--script=") {
			hasScript = true
		}
		if strings.HasPrefix(arg, "--script-opts=") {
			hasScriptOpts = true
			if !strings.Contains(arg, "skip-op_start=45.000000") || !strings.Contains(arg, "skip-op_end=135.000000") {
				t.Errorf("script opts missing op boundaries: %s", arg)
			}
			if !strings.Contains(arg, "skip-auto_intro=1") || !strings.Contains(arg, "skip-auto_ending=1") {
				t.Errorf("script opts missing auto flags: %s", arg)
			}
		}
	}
	if !hasScript || !hasScriptOpts {
		t.Errorf("missing --script or --script-opts in args: %v", args)
	}
}

type mockRoundTripper func(*http.Request) (*http.Response, error)

func (m mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m(req)
}

func TestGetSkipArgs_AniSkipIntegration(t *testing.T) {
	aniskipHTTP := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "graphql.anilist.co" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(`{"data":{"Media":{"id":154587,"idMal":52991}}}`)),
					Header:     make(http.Header),
				}, nil
			}
			if strings.Contains(req.URL.Path, "/skip-times/52991/1") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(bytes.NewBufferString(`{
						"found": true,
						"results": [
							{"interval": {"start_time": 90.0, "end_time": 180.0}, "skip_type": "op"},
							{"interval": {"start_time": 1350.0, "end_time": 1440.0}, "skip_type": "ed"}
						]
					}`)),
					Header: make(http.Header),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Header: make(http.Header)}, nil
		}),
	}

	aniskipClient := aniskip.NewClient(aniskipHTTP)
	clients := SkipClients{AniSkip: aniskipClient, HTTP: aniskipHTTP}
	args, scriptPath := getSkipArgs(clients, SkipSettings{
		Provider:       "aniskip",
		AutoSkipIntro:  true,
		AutoSkipEnding: true,
	}, model.ResolvedMedia{
		SeriesTitle:   "Frieren Unique Test Series",
		EpisodeNumber: 1,
	}, newTestSkipCache())
	defer cleanupSkipScript(scriptPath)

	if len(args) != 2 || scriptPath == "" {
		t.Fatalf("expected 2 args and non-empty scriptPath, got args=%v path=%s", args, scriptPath)
	}
	foundOpts := false
	for _, a := range args {
		if strings.HasPrefix(a, "--script-opts=") {
			foundOpts = true
			if !strings.Contains(a, "skip-op_start=90.000000") || !strings.Contains(a, "skip-op_end=180.000000") {
				t.Errorf("script opts missing op boundaries: %s", a)
			}
			if !strings.Contains(a, "skip-ed_start=1350.000000") || !strings.Contains(a, "skip-ed_end=1440.000000") {
				t.Errorf("script opts missing ed boundaries: %s", a)
			}
		}
	}
	if !foundOpts {
		t.Errorf("expected --script-opts in args: %v", args)
	}
}

func TestGetSkipArgs_SkipDBIntegration(t *testing.T) {
	skipdbHTTP := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "/api/segments") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(bytes.NewBufferString(`{
						"imdb_id": "tt0903747",
						"season": 1,
						"episode": 1,
						"segments": {
							"intro": {"start_ms": 120000, "end_ms": 150000},
							"recap": null,
							"outro": {"start_ms": 2700000, "end_ms": 2760000},
							"preview": null
						}
					}`)),
					Header: make(http.Header),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Header: make(http.Header)}, nil
		}),
	}

	skipdbClient := skipdb.NewClient(skipdbHTTP, "skdb_test_key")
	clients := SkipClients{SkipDB: skipdbClient, HTTP: skipdbHTTP}
	args, scriptPath := getSkipArgs(clients, SkipSettings{
		Provider: "skipdb",
	}, model.ResolvedMedia{
		SeriesTitle:   "Breaking Bad",
		SeriesURL:     "tt0903747",
		SeasonNumber:  1,
		EpisodeNumber: 1,
	}, newTestSkipCache())
	defer cleanupSkipScript(scriptPath)

	if len(args) != 2 || scriptPath == "" {
		t.Fatalf("expected skipdb args, got args=%v path=%s", args, scriptPath)
	}
	foundOpts := false
	for _, a := range args {
		if strings.HasPrefix(a, "--script-opts=") {
			foundOpts = true
			if !strings.Contains(a, "skip-op_start=120.000000") || !strings.Contains(a, "skip-op_end=150.000000") {
				t.Errorf("script opts missing op boundaries: %s", a)
			}
			if !strings.Contains(a, "skip-ed_start=2700.000000") || !strings.Contains(a, "skip-ed_end=2760.000000") {
				t.Errorf("script opts missing ed boundaries: %s", a)
			}
		}
	}
	if !foundOpts {
		t.Errorf("expected --script-opts in args: %v", args)
	}
}

func TestGetSkipArgs_IntroDBMovieIntegration(t *testing.T) {
	introdbHTTP := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "/segments") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(bytes.NewBufferString(`{
						"imdb_id": "tt1375666",
						"media_type": "movie",
						"is_movie": true,
						"intro": null,
						"recap": null,
						"outro": {"start_sec": 8400.0, "end_sec": 8800.0},
						"post_credits": null
					}`)),
					Header: make(http.Header),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Header: make(http.Header)}, nil
		}),
	}

	introdbClient := introdb.NewClient(introdbHTTP, "idb_test_key")
	clients := SkipClients{IntroDB: introdbClient, HTTP: introdbHTTP}
	args, scriptPath := getSkipArgs(clients, SkipSettings{
		Provider: "introdb",
	}, model.ResolvedMedia{
		SeriesTitle: "Inception",
		SeriesURL:   "tt1375666",
		MediaType:   "movie",
	}, newTestSkipCache())
	defer cleanupSkipScript(scriptPath)

	if len(args) != 2 || scriptPath == "" {
		t.Fatalf("expected introdb movie args, got args=%v path=%s", args, scriptPath)
	}
	foundOpts := false
	for _, a := range args {
		if strings.HasPrefix(a, "--script-opts=") {
			foundOpts = true
			if !strings.Contains(a, "skip-ed_start=8400.000000") || !strings.Contains(a, "skip-ed_end=8800.000000") {
				t.Errorf("script opts missing movie outro boundaries: %s", a)
			}
		}
	}
	if !foundOpts {
		t.Errorf("expected --script-opts in args: %v", args)
	}
}

func TestGetSkipArgs_AnimeSkipIntegration(t *testing.T) {
	askipHTTP := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			bodyBytes, _ := io.ReadAll(req.Body)
			bodyStr := string(bodyBytes)
			if strings.Contains(bodyStr, "findShowsByExternalId") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(bytes.NewBufferString(`{
						"data": {"findShowsByExternalId": [{"id": "show1"}]}
					}`)),
					Header: make(http.Header),
				}, nil
			}
			if strings.Contains(bodyStr, "findEpisodesByShowId") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(bytes.NewBufferString(`{
						"data": {"findEpisodesByShowId": [{"id": "ep1", "number": "1", "name": "Pilot"}]}
					}`)),
					Header: make(http.Header),
				}, nil
			}
			if strings.Contains(bodyStr, "findTimestampsByEpisodeId") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(bytes.NewBufferString(`{
						"data": {"findTimestampsByEpisodeId": [
							{"at": 0, "type": {"name": "Canon"}},
							{"at": 60, "type": {"name": "Opening"}},
							{"at": 150, "type": {"name": "Canon"}},
							{"at": 1300, "type": {"name": "Ending"}},
							{"at": 1390, "type": {"name": "Preview"}}
						]}
					}`)),
					Header: make(http.Header),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(`{}`)), Header: make(http.Header)}, nil
		}),
	}

	askipClient, err := animeskip.NewClient(askipHTTP, "test-client-id")
	if err != nil {
		t.Fatalf("unexpected error creating animeskip client: %v", err)
	}
	clients := SkipClients{AnimeSkip: askipClient, HTTP: askipHTTP}
	args, scriptPath := getSkipArgs(clients, SkipSettings{
		Provider: "anime-skip",
	}, model.ResolvedMedia{
		SeriesTitle:   "Sousou no Frieren",
		SeriesURL:     "154587",
		EpisodeNumber: 1,
		MediaType:     "anime",
	}, newTestSkipCache())
	defer cleanupSkipScript(scriptPath)

	if len(args) != 2 || scriptPath == "" {
		t.Fatalf("expected anime-skip args, got args=%v path=%s", args, scriptPath)
	}
	foundOpts := false
	for _, a := range args {
		if strings.HasPrefix(a, "--script-opts=") {
			foundOpts = true
			if !strings.Contains(a, "skip-op_start=60.000000") || !strings.Contains(a, "skip-op_end=150.000000") {
				t.Errorf("script opts missing op boundaries: %s", a)
			}
			if !strings.Contains(a, "skip-ed_start=1300.000000") || !strings.Contains(a, "skip-ed_end=1390.000000") {
				t.Errorf("script opts missing ed boundaries: %s", a)
			}
		}
	}
	if !foundOpts {
		t.Errorf("expected --script-opts in args: %v", args)
	}
}
