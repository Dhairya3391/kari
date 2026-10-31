package player

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"kari/internal/animeskip"
	"kari/internal/aniskip"
	"kari/internal/introdb"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/skipdb"
	"kari/internal/util"
)

func newTestSkipCache() *util.BoundedCache[combinedSkipTimes] {
	return util.NewBoundedCache[combinedSkipTimes](100)
}

func TestSelectConsensusPrefersMostRepeatedTiming(t *testing.T) {
	candidates := []skipCandidate{
		{start: 68, end: 158, source: "AniSkip", priority: 1},
		{start: 67, end: 157, source: "SkipDB", priority: 2},
		{start: 64, end: 156, source: "IntroDB", priority: 3},
		{start: 77, end: 167, source: "Anime-Skip", priority: 4},
	}
	selected, count, ok := selectConsensus(candidates)
	if !ok || count != 4 || selected.source != "AniSkip" || selected.start != 68 {
		t.Fatalf("selectConsensus() = (%+v, %d, %t), want preferred AniSkip timing", selected, count, ok)
	}
}

func TestSelectConsensusAcceptsStronglyOverlappingTimings(t *testing.T) {
	candidates := []skipCandidate{
		{start: 68.315, end: 158.315, source: "AniSkip", priority: 1},
		{start: 77.079, end: 167.152, source: "Anime-Skip", priority: 4},
	}
	selected, count, ok := selectConsensus(candidates)
	if !ok || count != 2 || selected.source != "AniSkip" {
		t.Fatalf("selectConsensus() = (%+v, %d, %t), want preferred AniSkip interval", selected, count, ok)
	}
}

func TestSelectConsensusUsesHighestPriorityAvailableTiming(t *testing.T) {
	cases := []struct {
		name       string
		candidates []skipCandidate
		wantSource string
		wantCount  int
	}{
		{
			name:       "single source",
			candidates: []skipCandidate{{start: 100, end: 190, source: "AniSkip", priority: 1}},
			wantSource: "AniSkip",
			wantCount:  1,
		},
		{
			name: "priority breaks a conflicting vote tie",
			candidates: []skipCandidate{
				{start: 100, end: 190, source: "SkipDB", priority: 2},
				{start: 101, end: 191, source: "IntroDB", priority: 3},
				{start: 130, end: 220, source: "AniSkip", priority: 1},
				{start: 131, end: 221, source: "Anime-Skip", priority: 4},
			},
			wantSource: "AniSkip",
			wantCount:  2,
		},
		{
			name: "provider priority beats a larger lower-priority group",
			candidates: []skipCandidate{
				{start: 100, end: 190, source: "AniSkip", priority: 1},
				{start: 130, end: 220, source: "SkipDB", priority: 2},
				{start: 131, end: 221, source: "IntroDB", priority: 3},
			},
			wantSource: "AniSkip",
			wantCount:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selected, count, ok := selectConsensus(tc.candidates)
			if !ok || count != tc.wantCount || selected.source != tc.wantSource {
				t.Fatalf("selectConsensus() = (%+v, %d, %t), want %s with %d matching source(s)", selected, count, ok, tc.wantSource, tc.wantCount)
			}
		})
	}
}

func TestSkipInfoStringShowsOnlyUsedSources(t *testing.T) {
	got := (SkipInfo{Opening: "AniSkip", Ending: "SkipDB", Recap: "AniSkip"}).String()
	if got != "AniSkip + SkipDB" {
		t.Fatalf("SkipInfo.String() = %q", got)
	}
}

func TestHybridUsesConsensusAndReportsIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		introStart int
		wantStart  string
		wantInfo   string
		wantRecap  string
	}{
		{name: "agreement and mixed chapter providers", introStart: 102, wantStart: "100.000000", wantInfo: "SkipDB + IntroDB", wantRecap: "skip-recap_start=30.000000"},
		{name: "disagreement uses provider priority", introStart: 130, wantStart: "100.000000", wantInfo: "SkipDB + IntroDB", wantRecap: "skip-recap_start=30.000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientHTTP := &http.Client{Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
				var body string
				switch req.URL.Host {
				case "api.skipdb.tv":
					body = `{"imdb_id":"tt1234567","season":1,"episode":1,"segments":{"intro":{"start_ms":100000,"end_ms":190000,"match":"exact"}}}`
				case "api.introdb.app":
					body = `{"imdb_id":"tt1234567","media_type":"tv","is_movie":false,"season":1,"episode":1,"intro":{"start_sec":` + fmt.Sprint(tc.introStart) + `,"end_sec":` + fmt.Sprint(tc.introStart+90) + `},"recap":{"start_sec":30,"end_sec":45}}`
				default:
					t.Fatalf("unexpected skip source host: %s", req.URL.Host)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			clients := SkipClients{
				SkipDB:  skipdb.NewClient(clientHTTP, "test"),
				IntroDB: introdb.NewClient(clientHTTP, "test"),
				HTTP:    clientHTTP,
			}
			registry := &Registry{skipClients: clients, skipSettings: SkipSettings{Provider: "hybrid"}, skipCache: newTestSkipCache()}
			media := model.ResolvedMedia{SeriesTitle: "Test", SeriesURL: "tt1234567", MediaType: "tv", SeasonNumber: 1, EpisodeNumber: 1}
			info, conclusive := registry.ResolveSkipInfo(media)
			if !conclusive {
				t.Fatal("ResolveSkipInfo() reported an incomplete lookup for mocked providers")
			}
			if got := info.String(); got != tc.wantInfo {
				t.Fatalf("ResolveSkipInfo() = %q, want %q", got, tc.wantInfo)
			}
			args, script := getSkipArgs(clients, registry.skipSettings, media, registry.skipCache)
			defer cleanupSkipScript(script)
			if tc.wantStart == "" {
				if len(args) != 0 || script != "" {
					t.Fatalf("disagreement produced playable skip args: %v", args)
				}
				return
			}
			for _, arg := range args {
				if !strings.HasPrefix(arg, "--script-opts=") {
					continue
				}
				if !strings.Contains(arg, "skip-op_start="+tc.wantStart) {
					t.Fatalf("hybrid selected wrong opening boundary: %s", arg)
				}
				if tc.wantRecap != "" && !strings.Contains(arg, tc.wantRecap) {
					t.Fatalf("hybrid omitted recap from another provider: %s", arg)
				}
			}
		})
	}
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

func TestGetSkipArgs_RetriesAfterTransientFailure(t *testing.T) {
	failAniList := true
	var anilistRequests, aniskipRequests int
	httpClient := &http.Client{Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "graphql.anilist.co":
			anilistRequests++
			if failAniList {
				return nil, fmt.Errorf("temporary anilist failure")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"data":{"Media":{"id":105310,"idMal":38671}}}`)),
				Header:     make(http.Header),
			}, nil
		case "api.aniskip.com":
			aniskipRequests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"found":true,"results":[{"interval":{"start_time":190.188,"end_time":280.188},"skip_type":"op"}]}`)),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("unexpected skip source host: %s", req.URL.Host)
			return nil, nil
		}
	})}
	clients := SkipClients{AniSkip: aniskip.NewClient(httpClient), HTTP: httpClient}
	settings := SkipSettings{Provider: "aniskip"}
	cache := newTestSkipCache()
	media := model.ResolvedMedia{SeriesTitle: "Fire Force", SeriesURL: "105310", MediaType: "anime", SeasonNumber: 1, EpisodeNumber: 9}

	args, script := getSkipArgs(clients, settings, media, cache)
	if len(args) != 0 || script != "" {
		cleanupSkipScript(script)
		t.Fatalf("failed lookup should not produce skip args, got %v", args)
	}
	if _, ok := cache.Get(skipCacheKey(media, settings.Provider)); ok {
		t.Fatal("transient failure must not be cached as empty skip data")
	}

	failAniList = false
	args, script = getSkipArgs(clients, settings, media, cache)
	defer cleanupSkipScript(script)
	if len(args) == 0 {
		t.Fatal("expected a retry to recover skip args after the network recovers")
	}
	if anilistRequests != 2 || aniskipRequests != 1 {
		t.Fatalf("requests = (anilist=%d, aniskip=%d), want (2, 1)", anilistRequests, aniskipRequests)
	}
}

func TestResolveSkipInfoReportsWhetherLookupWasConclusive(t *testing.T) {
	media := model.ResolvedMedia{
		SeriesTitle:   "Fire Force",
		SeriesURL:     "105310",
		MediaType:     "anime",
		SeasonNumber:  1,
		EpisodeNumber: 9,
	}
	newRegistry := func(handler mockRoundTripper) *Registry {
		httpClient := &http.Client{Transport: handler}
		return &Registry{
			skipClients:  SkipClients{AniSkip: aniskip.NewClient(httpClient), HTTP: httpClient},
			skipSettings: SkipSettings{Provider: "aniskip"},
			skipCache:    newTestSkipCache(),
		}
	}

	t.Run("provider failure is inconclusive", func(t *testing.T) {
		registry := newRegistry(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("temporary network failure")
		})
		if _, conclusive := registry.ResolveSkipInfo(media); conclusive {
			t.Fatal("failed lookup must be reported as inconclusive so callers can retry")
		}
	})

	t.Run("confirmed absence is conclusive", func(t *testing.T) {
		registry := newRegistry(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "graphql.anilist.co" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"data":{"Media":{"id":105310,"idMal":38671}}}`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		})
		info, conclusive := registry.ResolveSkipInfo(media)
		if !conclusive {
			t.Fatal("confirmed absence must be reported as conclusive")
		}
		if info.String() != "none" {
			t.Fatalf("SkipInfo.String() = %q, want none", info.String())
		}
	})
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
				body, _ := io.ReadAll(req.Body)
				if !strings.Contains(string(body), `"id":154587`) {
					t.Fatalf("AniSkip must resolve the selected AniList ID: %s", body)
				}
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
		SeriesURL:     "154587",
		EpisodeNumber: 1,
		MediaType:     "anime",
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

func TestBuildSkipArgsFromTimes_ClampsEndingBeforePostCredits(t *testing.T) {
	times := combinedSkipTimes{
		EdStart:      7000.0,
		EdEnd:        7500.0, // Erroneously extends past PreviewStart
		PreviewStart: 7200.0, // Post-credits start
		PreviewEnd:   7260.0,
	}
	settings := SkipSettings{
		Provider:       "hybrid",
		AutoSkipEnding: true,
		SkipPreview:    false, // Ask
	}

	args, scriptPath := buildSkipArgsFromTimes(times, settings)
	defer cleanupSkipScript(scriptPath)

	for _, a := range args {
		if strings.HasPrefix(a, "--script-opts=") {
			if !strings.Contains(a, "skip-ed_end=7200.000000") {
				t.Errorf("expected ed_end to clamp to preview_start (7200), got: %s", a)
			}
			if !strings.Contains(a, "skip-preview_start=7200.000000") {
				t.Errorf("expected preview_start to remain 7200, got: %s", a)
			}
			if !strings.Contains(a, "skip-auto_preview=0") {
				t.Errorf("expected auto_preview to be 0 (ask), got: %s", a)
			}
		}
	}
}

func TestGetSkipArgs_LongOutroDoesNotSynthesizePreview(t *testing.T) {
	introdbHTTP := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(bytes.NewBufferString(`{
					"imdb_id": "tt2861424",
					"season": 1,
					"episode": 2,
					"intro": {"start_sec": 0, "end_sec": 30},
					"outro": {"start_sec": 1246, "end_sec": 1318},
					"post_credits": null
				}`)),
				Header: make(http.Header),
			}, nil
		}),
	}

	introdbClient := introdb.NewClient(introdbHTTP, "test-api-key")
	clients := SkipClients{IntroDB: introdbClient, HTTP: introdbHTTP}
	args, scriptPath := getSkipArgs(clients, SkipSettings{
		Provider:       "introdb",
		AutoSkipEnding: true,
		SkipPreview:    false, // Ask
	}, model.ResolvedMedia{
		SeriesTitle:   "Rick and Morty",
		SeriesURL:     "tt2861424",
		SeasonNumber:  1,
		EpisodeNumber: 2,
		MediaType:     "tv",
	}, newTestSkipCache())
	defer cleanupSkipScript(scriptPath)

	foundOpts := false
	for _, a := range args {
		if strings.HasPrefix(a, "--script-opts=") {
			foundOpts = true
			// No upstream post-credits label: ending stays whole, no phantom preview is synthesized.
			if !strings.Contains(a, "skip-ed_start=1246.000000") || !strings.Contains(a, "skip-ed_end=1318.000000") {
				t.Errorf("expected full ed 1246-1318 without split, got: %s", a)
			}
			if strings.Contains(a, "skip-preview_start=1274.000000") {
				t.Errorf("must not synthesize preview from long outro, got: %s", a)
			}
		}
	}
	if !foundOpts {
		t.Fatal("expected --script-opts in args")
	}
}

func TestResolveSourceSkipArgsProviderSegments(t *testing.T) {
	source := provider.MediaSource{
		Resolver: "segment-provider",
		SkipTimes: &provider.SkipTimes{
			Source:  "Segment Provider",
			OpStart: 15.5,
			OpEnd:   105.5,
			EdStart: 1420.0,
			EdEnd:   1510.0,
		},
	}
	settings := SkipSettings{
		Provider:      "hybrid",
		AutoSkipIntro: true,
	}
	media := model.ResolvedMedia{
		SeriesTitle:   "Frieren",
		EpisodeNumber: 1,
	}

	args, scriptPath, hasProviderSegments := resolveSourceSkipArgs(source, settings, media)
	defer cleanupSkipScript(scriptPath)
	if !hasProviderSegments {
		t.Fatal("expected resolveSourceSkipArgs to use provider-declared segments")
	}
	if scriptPath == "" {
		t.Fatal("expected non-empty scriptPath")
	}

	found := false
	for _, a := range args {
		if strings.HasPrefix(a, "--script-opts=") {
			found = true
			if !strings.Contains(a, "skip-op_start=15.500000") || !strings.Contains(a, "skip-op_end=105.500000") {
				t.Errorf("expected op 15.5-105.5, got: %s", a)
			}
			if !strings.Contains(a, "skip-ed_start=1420.000000") || !strings.Contains(a, "skip-ed_end=1510.000000") {
				t.Errorf("expected ed 1420-1510, got: %s", a)
			}
			if !strings.Contains(a, "skip-auto_intro=1") {
				t.Errorf("expected auto_intro=1, got: %s", a)
			}
		}
	}
	if !found {
		t.Fatal("expected --script-opts in args")
	}
}

func TestResolveSourceSkipArgsAnyProviderMayDeclareSegments(t *testing.T) {
	source := provider.MediaSource{
		Resolver: "another-provider",
		SkipTimes: &provider.SkipTimes{
			Source:  "Another Provider",
			OpStart: 15.5,
			OpEnd:   105.5,
		},
	}
	settings := SkipSettings{Provider: "hybrid"}
	media := model.ResolvedMedia{SeriesTitle: "Frieren", EpisodeNumber: 1}

	args, scriptPath, hasProviderSegments := resolveSourceSkipArgs(source, settings, media)
	defer cleanupSkipScript(scriptPath)
	if !hasProviderSegments || len(args) == 0 || scriptPath == "" {
		cleanupSkipScript(scriptPath)
		t.Fatalf("provider-declared segments must be used, got present=%t args=%v", hasProviderSegments, args)
	}
}

func TestResolveSourceSkipArgsSourceWithoutSegments(t *testing.T) {
	source := provider.MediaSource{
		Resolver:  "some-provider",
		SkipTimes: nil,
	}
	settings := SkipSettings{Provider: "hybrid"}
	media := model.ResolvedMedia{SeriesTitle: "Frieren", EpisodeNumber: 1}

	args, scriptPath, hasProviderSegments := resolveSourceSkipArgs(source, settings, media)
	if hasProviderSegments || len(args) != 0 || scriptPath != "" {
		cleanupSkipScript(scriptPath)
		t.Fatalf("source without segments must be ignored, got present=%t args=%v", hasProviderSegments, args)
	}
}

func TestResolveSourceSkipArgs_OffProvider(t *testing.T) {
	source := provider.MediaSource{
		Resolver: "miruro",
		SkipTimes: &provider.SkipTimes{
			OpStart: 10,
			OpEnd:   100,
		},
	}
	settings := SkipSettings{Provider: "off"}
	media := model.ResolvedMedia{SeriesTitle: "Frieren", EpisodeNumber: 1}

	args, scriptPath, isMiruro := resolveSourceSkipArgs(source, settings, media)
	if isMiruro || len(args) != 0 || scriptPath != "" {
		cleanupSkipScript(scriptPath)
		t.Fatalf("off provider must yield no skip args, got isMiruro=%t, args=%v", isMiruro, args)
	}
}

func TestResolveSkipInfoProviderSourceSegments(t *testing.T) {
	registry := NewRegistry("", SkipClients{}, SkipSettings{Provider: "hybrid"})
	media := model.ResolvedMedia{
		SeriesTitle:   "Sousou no Frieren",
		EpisodeNumber: 1,
		Playback: []provider.MediaSource{
			{
				Resolver: "segment-provider",
				SkipTimes: &provider.SkipTimes{
					Source:  "Segment Provider",
					OpStart: 0,
					OpEnd:   89,
					EdStart: 1460,
					EdEnd:   1549,
				},
			},
		},
	}

	info, conclusive := registry.ResolveSkipInfo(media)
	if !conclusive {
		t.Fatal("expected conclusive = true for miruro source with segments")
	}
	if info.Opening != "Segment Provider" || info.Ending != "Segment Provider" {
		t.Errorf("info = %+v, want provider provenance", info)
	}
	if got := info.String(); got != "Segment Provider" {
		t.Errorf("info.String() = %q, want provider provenance", got)
	}
	if info.Recap != "" || info.Preview != "" {
		t.Errorf("expected absent recap and preview to be empty, got Recap=%q Preview=%q", info.Recap, info.Preview)
	}

	// A segment source is ignored when it does not match the active resolver.
	mediaAnikoto := model.ResolvedMedia{
		SeriesTitle:   "Sousou no Frieren",
		EpisodeNumber: 1,
		Resolver:      "other-provider",
		Playback: []provider.MediaSource{
			{
				Resolver: "segment-provider",
				SkipTimes: &provider.SkipTimes{
					Source:  "Segment Provider",
					OpStart: 0,
					OpEnd:   89,
				},
			},
		},
	}
	infoAnikoto, _ := registry.ResolveSkipInfo(mediaAnikoto)
	if infoAnikoto.Opening == "Segment Provider" {
		t.Errorf("non-active provider should not report segment skips, got %+v", infoAnikoto)
	}

	// Zero-valued optional segments (e.g. RecapStart=0, RecapEnd=0) are not treated as valid segments
	zeroOptionalsSource := provider.MediaSource{
		Resolver: "segment-provider",
		SkipTimes: &provider.SkipTimes{
			Source:       "Segment Provider",
			OpStart:      10,
			OpEnd:        100,
			RecapStart:   0,
			RecapEnd:     0,
			PreviewStart: 0,
			PreviewEnd:   0,
		},
	}
	args, scriptPath, isMiruro := resolveSourceSkipArgs(zeroOptionalsSource, SkipSettings{Provider: "hybrid"}, media)
	defer cleanupSkipScript(scriptPath)
	if !isMiruro {
		t.Fatal("expected isMiruro=true for valid op")
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--script-opts=") {
			if !strings.Contains(a, "skip-recap_start=-1.000000") {
				t.Errorf("zero-valued recap must be sent as -1.000000, got: %s", a)
			}
			if !strings.Contains(a, "skip-preview_start=-1.000000") {
				t.Errorf("zero-valued preview must be sent as -1.000000, got: %s", a)
			}
		}
	}
}
