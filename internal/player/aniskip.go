package player

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"kari/internal/animeskip"
	"kari/internal/aniskip"
	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/introdb"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/skipdb"
	"kari/internal/tmdb"
	"kari/internal/util"
)

// log scopes every line from this package/component.
var skipLog = logging.With("component", "player.skip")

// SkipClients bundles the external skip timestamp and metadata clients.
type SkipClients struct {
	AniSkip   *aniskip.Client
	AnimeSkip *animeskip.Client
	SkipDB    *skipdb.Client
	IntroDB   *introdb.Client
	TMDB      *tmdb.KeyPool
	HTTP      *http.Client
}

// SkipSettings holds the configured skip provider and per-zone auto-skip flags.
type SkipSettings struct {
	Provider       string // "hybrid", "skipdb", "introdb", "anime-skip", "aniskip", "off"
	AutoSkipIntro  bool
	AutoSkipEnding bool
	SkipRecap      bool // Automatically skips recap segments.
	SkipPreview    bool // Automatically skips preview segments.
}

// skipLookupTimeout bounds the whole skip fan-out. The AniSkip path needs
// two sequential API calls (AniList ID resolution, then timings), so a
// deadline tuned for a single request drops valid data on slow connections
// and then caches the empty result for the rest of the session.
const skipLookupTimeout = 6 * time.Second

const skipLuaScript = `
local opts = {
    recap_start   = -1,
    recap_end     = -1,
    op_start      = -1,
    op_end        = -1,
    ed_start      = -1,
    ed_end        = -1,
    preview_start = -1,
    preview_end   = -1,
    auto_recap    = 0,
    auto_intro    = 0,
    auto_ending   = 0,
    auto_preview  = 0,
    min_duration  = 60,
    is_live       = 0,
}

pcall(function()
    local mp_opt = require('mp.options')
    if mp_opt and mp_opt.read_options then
        mp_opt.read_options(opts, "skip")
    end
end)

-- ── Chapter injection ────────────────────────────────────────────────────────

local function build_chapters()
    local segments = {}

    if opts.recap_start >= 0 and opts.recap_end > opts.recap_start then
        table.insert(segments, { at = opts.recap_start,   label = "Recap" })
        table.insert(segments, { at = opts.recap_end,     label = "Episode" })
    end
    if opts.op_start >= 0 and opts.op_end > opts.op_start then
        table.insert(segments, { at = opts.op_start,      label = "Opening" })
        table.insert(segments, { at = opts.op_end,        label = "Episode" })
    end
    if opts.ed_start >= 0 and opts.ed_end > opts.ed_start then
        table.insert(segments, { at = opts.ed_start,      label = "Ending" })
        table.insert(segments, { at = opts.ed_end,        label = "Episode" })
    end
    if opts.preview_start >= 0 and opts.preview_end > opts.preview_start then
        local prev_label = "Preview"
        if opts.preview_start >= opts.ed_start and opts.ed_start >= 0 then
            prev_label = "Post-Credits"
        end
        table.insert(segments, { at = opts.preview_start, label = prev_label })
        table.insert(segments, { at = opts.preview_end,   label = "Episode" })
    end

    if #segments == 0 then
        return {}
    end

    table.sort(segments, function(a, b)
        if math.abs(a.at - b.at) > 0.1 then
            return a.at < b.at
        end
        if a.label ~= "Episode" and b.label == "Episode" then
            return true
        end
        return false
    end)

    local chapters = {}
    if segments[1].at > 0.5 then
        table.insert(chapters, { title = "Episode", time = 0 })
    end

    local last_time = -1
    local last_label = nil
    for _, seg in ipairs(segments) do
        if seg.label ~= last_label and (last_time < 0 or (seg.at - last_time) >= 0.5) then
            table.insert(chapters, { title = seg.label, time = math.max(0, seg.at) })
            last_time = seg.at
            last_label = seg.label
        elseif seg.label ~= "Episode" and last_label == "Episode" and (seg.at - last_time) < 0.5 then
            if #chapters > 0 then
                chapters[#chapters] = { title = seg.label, time = math.max(0, seg.at) }
                last_label = seg.label
            end
        end
    end
    return chapters
end

local chapters_injected = false

local function inject_chapters()
    if chapters_injected then return end
    local chapters = build_chapters()
    if #chapters == 0 then return end

    pcall(function()
        local existing = mp.get_property_native("chapter-list")
        if existing and #existing > 0 then
            local has_skip_chapters = false
            for _, ch in ipairs(existing) do
                local t = string.lower(ch.title or "")
                if t:find("opening") or t:find("intro") or t:find("ending") or t:find("outro") or t:find("credits") or t:find("recap") or t:find("preview") then
                    has_skip_chapters = true
                    break
                end
            end
            if has_skip_chapters then
                chapters_injected = true
                return
            end
        end
        mp.set_property_native("chapter-list", chapters)
        chapters_injected = true
    end)
end

mp.register_event("file-loaded", inject_chapters)
mp.register_event("playback-restart", inject_chapters)
pcall(function()
    mp.observe_property("duration", "number", function(_, d)
        if d and d > 0 and d <= opts.min_duration and opts.is_live == 0 then
            local path = mp.get_property("path") or ""
            if path:find("^https?://") then
                mp.osd_message("Filtering short warning video (" .. string.format("%.0f", d) .. "s)...", 2)
                pcall(function()
                    mp.commandv("quit", "2")
                end)
                return
            end
        end
        if d and d > 0 and not chapters_injected then
            inject_chapters()
        end
    end)
end)

-- ── Zone detection ───────────────────────────────────────────────────────────

local function active_zone(time)
    if not time then return nil, nil end
    if opts.recap_start >= 0 and time >= opts.recap_start and time < (opts.recap_end - 0.5) then
        return "recap", opts.recap_end
    end
    if opts.op_start >= 0 and time >= opts.op_start and time < (opts.op_end - 0.5) then
        return "intro", opts.op_end
    end
    if opts.preview_start >= 0 and time >= opts.preview_start and time < (opts.preview_end - 0.5) then
        return "preview", opts.preview_end
    end
    if opts.ed_start >= 0 and time >= opts.ed_start and time < (opts.ed_end - 0.5) then
        return "ending", opts.ed_end
    end
    return nil, nil
end

local function zone_label(zone)
    if zone == "preview" then
        if opts.preview_start >= opts.ed_start and opts.ed_start >= 0 then
            return "Post-Credits"
        end
        return "Preview"
    end
    if zone == "recap" then return "Recap" end
    if zone == "intro" then return "Opening" end
    if zone == "ending" then return "Ending" end
    return ""
end

local auto_flags = {
    recap   = function() return opts.auto_recap   == 1 end,
    intro   = function() return opts.auto_intro   == 1 end,
    ending  = function() return opts.auto_ending  == 1 end,
    preview = function() return opts.auto_preview == 1 end,
}

local last_zone = nil
local auto_fired = {}

pcall(function()
    mp.observe_property("time-pos", "number", function(_, time)
        if not time then return end

        local zone, end_time = active_zone(time)

        if zone then
            local label = zone_label(zone)

            if auto_flags[zone]() and not auto_fired[zone] then
                auto_fired[zone] = true
                pcall(function()
                    mp.commandv("seek", end_time, "absolute")
                    mp.osd_message(label .. " Skipped", 2)
                end)
                last_zone = nil
                return
            end

            if zone ~= last_zone then
                for z in pairs(auto_fired) do
                    if z ~= zone then auto_fired[z] = nil end
                end
            end

            pcall(function()
                mp.osd_message("Press 'Enter' to Skip " .. label, 1)
            end)
            last_zone = zone
        else
            if last_zone then
                auto_fired[last_zone] = nil
            end
            last_zone = nil
        end
    end)
end)

-- ── ENTER key binding ────────────────────────────────────────────────────────

local function do_skip()
    pcall(function()
        local time = mp.get_property_number("time-pos")
        if not time then return end

        local zone, end_time = active_zone(time)
        if zone and end_time then
            mp.commandv("seek", end_time, "absolute")
            mp.osd_message(zone_label(zone) .. " Skipped", 2)
        end
    end)
end

pcall(function()
    mp.add_forced_key_binding("ENTER", "kari-skip", do_skip)
end)
`

// combinedSkipTimes holds agreed intervals and their source provenance.
type combinedSkipTimes struct {
	OpStart       float64
	OpEnd         float64
	EdStart       float64
	EdEnd         float64
	RecapStart    float64
	RecapEnd      float64
	PreviewStart  float64
	PreviewEnd    float64
	IsLive        bool
	OpSource      string
	OpAgreement   int
	OpConflict    bool
	EdSource      string
	EdAgreement   int
	EdConflict    bool
	RecapSource   string
	RecapAgree    int
	RecapConflict bool
	PrevSource    string
	PrevAgree     int
	PrevConflict  bool
}

// SkipInfo describes the confirmed timing source for each chapter type.
type SkipInfo struct {
	// Opening describes the source selected for opening boundaries.
	Opening string
	// Ending describes the source selected for ending boundaries.
	Ending string
	// Recap describes the source selected for recap boundaries.
	Recap string
	// Preview describes the source selected for preview boundaries.
	Preview string
}

// String formats which sources supplied the timings used for playback.
func (s SkipInfo) String() string {
	providers := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, source := range []string{s.Opening, s.Ending, s.Recap, s.Preview} {
		if source == "" {
			continue
		}
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		providers = append(providers, source)
	}
	if len(providers) == 0 {
		return "none"
	}
	return strings.Join(providers, " + ")
}

type skipCandidate struct {
	start, end float64
	source     string
	priority   int
}

// An 80% interval overlap accepts boundary drift while still requiring the
// sources to identify substantially the same segment.
const skipAgreementOverlap = 0.8

func sameSkipInterval(a, b skipCandidate) bool {
	intersection := min(a.end, b.end) - max(a.start, b.start)
	if intersection <= 0 {
		return false
	}
	union := max(a.end, b.end) - min(a.start, b.start)
	return union > 0 && intersection/union >= skipAgreementOverlap
}

func selectConsensus(candidates []skipCandidate) (skipCandidate, int, bool) {
	bestCount := 0
	var best skipCandidate
	for _, candidate := range candidates {
		if candidate.start < 0 || candidate.end <= candidate.start {
			continue
		}
		count := 0
		for _, other := range candidates {
			if other.source != candidate.source && sameSkipInterval(candidate, other) {
				count++
			}
		}
		count++
		if bestCount == 0 || candidate.priority < best.priority ||
			(candidate.priority == best.priority && count > bestCount) {
			best, bestCount = candidate, count
		}
	}
	return best, bestCount, bestCount > 0
}

func infoText(source string) string {
	return source
}

var (
	imdbRegex = regexp.MustCompile(`(?i)\b(tt\d{7,10})\b`)
	imdbCache = util.NewBoundedCache[string](500)
)

func resolveIMDbID(ctx context.Context, media model.ResolvedMedia, keyPool *tmdb.KeyPool, httpClient *http.Client) (string, error) {
	// 1. Direct regex match on URLs or titles
	for _, candidate := range []string{media.SeriesURL, media.EpisodeURL, media.MediaURL, media.SeriesTitle} {
		if m := imdbRegex.FindString(candidate); m != "" {
			return strings.ToLower(m), nil
		}
	}

	if httpClient == nil {
		httpClient = httpclient.NewWithTimeout(5 * time.Second)
	}

	// 2. Resolve from TMDBID if available
	if media.TMDBID <= 0 || keyPool == nil {
		return "", nil
	}
	cacheKey := fmt.Sprintf("tmdb:%d:%s", media.TMDBID, media.MediaType)
	if cached, ok := imdbCache.Get(cacheKey); ok {
		return cached, nil
	}

	apiKey, err := keyPool.NextKey()
	if err != nil {
		return "", fmt.Errorf("tmdb api key: %w", err)
	}
	if apiKey == "" {
		return "", nil
	}
	mediaType := "tv"
	if media.MediaType == "movie" || (media.MediaType != "tv" && media.MediaType != "anime" && media.MediaType != "cartoon" && media.SeasonNumber == 0 && media.EpisodeNumber <= 1) {
		mediaType = "movie"
	}
	endpoint := fmt.Sprintf("%s/%s/%d/external_ids?api_key=%s",
		config.TMDBAPIBase, mediaType, media.TMDBID, url.QueryEscape(apiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("tmdb external ids request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tmdb external ids fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tmdb external ids status: %d", resp.StatusCode)
	}
	var res struct {
		IMDbID string `json:"imdb_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("tmdb external ids decode: %w", err)
	}
	id := strings.ToLower(strings.TrimSpace(res.IMDbID))
	if id == "" {
		return "", nil
	}
	imdbCache.Set(cacheKey, id)
	return id, nil
}

// getSkipArgs resolves skip intervals according to settings and writes a
// temporary Lua script for MPV. Returns MPV arguments and script path.
func getSkipArgs(
	clients SkipClients,
	settings SkipSettings,
	media model.ResolvedMedia,
	cache *util.BoundedCache[combinedSkipTimes],
) ([]string, string) {
	providerMode := strings.ToLower(strings.TrimSpace(settings.Provider))
	if providerMode == "" {
		providerMode = "hybrid"
	}
	if providerMode == "off" || providerMode == "none" {
		return nil, ""
	}
	if strings.TrimSpace(media.SeriesTitle) == "" && media.TMDBID == 0 {
		return nil, ""
	}

	cacheKey := skipCacheKey(media, providerMode)
	if cached, ok := cache.Get(cacheKey); ok {
		if cached.OpStart < 0 && cached.EdStart < 0 && cached.RecapStart < 0 && cached.PreviewStart < 0 {
			return nil, ""
		}
		return buildSkipArgsFromTimes(cached, settings)
	}

	ctx, cancel := context.WithTimeout(context.Background(), skipLookupTimeout)
	defer cancel()

	// 1. Resolve AniList ID from media metadata if available
	var anilistID int
	if trimmed := strings.Trim(strings.TrimSpace(media.SeriesURL), "/"); trimmed != "" {
		if id, err := strconv.Atoi(trimmed); err == nil && id > 0 {
			anilistID = id
		}
	}

	isMovie := media.MediaType == "movie" || (media.SeasonNumber == 0 && media.EpisodeNumber == 0 && media.MediaType != "tv" && media.MediaType != "anime")
	isAnime := media.MediaType == "anime" || anilistID > 0
	times := combinedSkipTimes{
		OpStart: -1, OpEnd: -1,
		EdStart: -1, EdEnd: -1,
		RecapStart: -1, RecapEnd: -1,
		PreviewStart: -1, PreviewEnd: -1,
		IsLive: media.IsLive(),
	}

	var (
		g            errgroup.Group
		lookupFailed atomic.Bool
		imdbID       string
		askipTimes   *animeskip.SkipTimes
		aniskipRes   *aniskip.SkipTimes
		skipdbRes    *skipdb.SkipTimes
		introdbRes   *introdb.SkipTimes
	)

	// Anime-Skip
	if (providerMode == "hybrid" || providerMode == "anime-skip") && clients.AnimeSkip != nil && isAnime && !isMovie {
		g.Go(func() error {
			aniIDStr := ""
			if anilistID > 0 {
				aniIDStr = strconv.Itoa(anilistID)
			}
			t, err := clients.AnimeSkip.GetTimestamps(ctx, aniIDStr, media.SeasonNumber, media.EpisodeNumber, media.SeriesTitle, media.EpisodeTitle)
			if err != nil {
				lookupFailed.Store(true)
				skipLog.Debug("anime-skip lookup error", "err", err)
			} else {
				askipTimes = t
			}
			return nil
		})
	}

	// AniSkip
	if (providerMode == "hybrid" || providerMode == "aniskip") && clients.AniSkip != nil && isAnime && !isMovie {
		g.Go(func() error {
			var malID int
			var err error
			if anilistID > 0 {
				_, malID, err = clients.AniSkip.GetIDsByAniListID(ctx, anilistID)
			} else if strings.TrimSpace(media.SeriesTitle) != "" {
				_, malID, err = clients.AniSkip.GetIDs(ctx, media.SeriesTitle)
			}
			if err != nil {
				lookupFailed.Store(true)
				skipLog.Debug("aniskip id lookup error", "err", err)
				return nil
			}
			if malID > 0 {
				t, err := clients.AniSkip.GetSkipTimes(ctx, malID, media.EpisodeNumber)
				if err != nil {
					lookupFailed.Store(true)
					skipLog.Debug("aniskip lookup error", "err", err)
				} else {
					aniskipRes = t
				}
			}
			return nil
		})
	}

	// SkipDB & IntroDB (requires IMDb ID)
	needIMDb := (providerMode == "hybrid" || providerMode == "skipdb" || providerMode == "introdb") &&
		(clients.SkipDB != nil || clients.IntroDB != nil)
	if needIMDb {
		g.Go(func() error {
			id, err := resolveIMDbID(ctx, media, clients.TMDB, clients.HTTP)
			if err != nil {
				lookupFailed.Store(true)
				skipLog.Debug("imdb id lookup error", "err", err)
				return nil
			}
			if id == "" {
				return nil
			}
			imdbID = id
			var subG errgroup.Group
			if (providerMode == "hybrid" || providerMode == "skipdb") && clients.SkipDB != nil {
				subG.Go(func() error {
					t, err := clients.SkipDB.GetSegments(ctx, imdbID, media.SeasonNumber, media.EpisodeNumber, isMovie)
					if err != nil {
						lookupFailed.Store(true)
						skipLog.Debug("skipdb lookup error", "err", err)
					} else {
						skipdbRes = t
					}
					return nil
				})
			}
			if (providerMode == "hybrid" || providerMode == "introdb") && clients.IntroDB != nil {
				subG.Go(func() error {
					t, err := clients.IntroDB.GetSegments(ctx, imdbID, media.SeasonNumber, media.EpisodeNumber, isMovie)
					if err != nil {
						lookupFailed.Store(true)
						skipLog.Debug("introdb lookup error", "err", err)
					} else {
						introdbRes = t
					}
					return nil
				})
			}
			_ = subG.Wait()
			return nil
		})
	}
	_ = g.Wait()

	applyAnimeSkip := func() {
		if askipTimes != nil {
			if times.OpStart < 0 && askipTimes.OpStart >= 0 {
				times.OpStart, times.OpEnd = askipTimes.OpStart, askipTimes.OpEnd
				times.OpSource, times.OpAgreement = "Anime-Skip", 1
			}
			if times.EdStart < 0 && askipTimes.EdStart >= 0 {
				times.EdStart, times.EdEnd = askipTimes.EdStart, askipTimes.EdEnd
				times.EdSource, times.EdAgreement = "Anime-Skip", 1
			}
			if times.RecapStart < 0 && askipTimes.RecapStart >= 0 {
				times.RecapStart, times.RecapEnd = askipTimes.RecapStart, askipTimes.RecapEnd
				times.RecapSource, times.RecapAgree = "Anime-Skip", 1
			}
			if times.PreviewStart < 0 && askipTimes.PreviewStart >= 0 {
				times.PreviewStart, times.PreviewEnd = askipTimes.PreviewStart, askipTimes.PreviewEnd
				times.PrevSource, times.PrevAgree = "Anime-Skip", 1
			}
		}
	}

	applyAniSkip := func() {
		if aniskipRes != nil {
			if times.OpStart < 0 && aniskipRes.OpStart >= 0 {
				times.OpStart, times.OpEnd = aniskipRes.OpStart, aniskipRes.OpEnd
				times.OpSource, times.OpAgreement = "AniSkip", 1
			}
			if times.EdStart < 0 && aniskipRes.EdStart >= 0 {
				times.EdStart, times.EdEnd = aniskipRes.EdStart, aniskipRes.EdEnd
				times.EdSource, times.EdAgreement = "AniSkip", 1
			}
		}
	}

	applySkipDB := func() {
		if skipdbRes != nil {
			if times.OpStart < 0 && skipdbRes.OpStart >= 0 {
				times.OpStart, times.OpEnd = skipdbRes.OpStart, skipdbRes.OpEnd
				times.OpSource, times.OpAgreement = "SkipDB", 1
			}
			if times.EdStart < 0 && skipdbRes.EdStart >= 0 {
				times.EdStart, times.EdEnd = skipdbRes.EdStart, skipdbRes.EdEnd
				times.EdSource, times.EdAgreement = "SkipDB", 1
			}
			if times.RecapStart < 0 && skipdbRes.RecapStart >= 0 {
				times.RecapStart, times.RecapEnd = skipdbRes.RecapStart, skipdbRes.RecapEnd
				times.RecapSource, times.RecapAgree = "SkipDB", 1
			}
			if times.PreviewStart < 0 && skipdbRes.PreviewStart >= 0 {
				times.PreviewStart, times.PreviewEnd = skipdbRes.PreviewStart, skipdbRes.PreviewEnd
				times.PrevSource, times.PrevAgree = "SkipDB", 1
			}
		}
	}

	applyIntroDB := func() {
		if introdbRes != nil {
			if times.OpStart < 0 && introdbRes.OpStart >= 0 {
				times.OpStart, times.OpEnd = introdbRes.OpStart, introdbRes.OpEnd
				times.OpSource, times.OpAgreement = "IntroDB", 1
			}
			if times.EdStart < 0 && introdbRes.EdStart >= 0 {
				times.EdStart, times.EdEnd = introdbRes.EdStart, introdbRes.EdEnd
				times.EdSource, times.EdAgreement = "IntroDB", 1
			}
			if times.RecapStart < 0 && introdbRes.RecapStart >= 0 {
				times.RecapStart, times.RecapEnd = introdbRes.RecapStart, introdbRes.RecapEnd
				times.RecapSource, times.RecapAgree = "IntroDB", 1
			}
			if times.PreviewStart < 0 && introdbRes.PreviewStart >= 0 {
				times.PreviewStart, times.PreviewEnd = introdbRes.PreviewStart, introdbRes.PreviewEnd
				times.PrevSource, times.PrevAgree = "IntroDB", 1
			}
		}
	}

	applyConsensus := func() {
		priority := map[string]int{"SkipDB": 1, "IntroDB": 2}
		if isAnime {
			priority = map[string]int{"AniSkip": 1, "SkipDB": 2, "IntroDB": 3, "Anime-Skip": 4}
		}
		selectZone := func(values ...skipCandidate) (float64, float64, string, int, bool) {
			valid := make([]skipCandidate, 0, len(values))
			for _, value := range values {
				if value.start >= 0 && value.end > value.start {
					valid = append(valid, value)
				}
			}
			selected, agreement, ok := selectConsensus(valid)
			if !ok {
				return -1, -1, "", 0, len(valid) > 0
			}
			return selected.start, selected.end, selected.source, agreement, false
		}
		candidate := func(source string, start, end float64) skipCandidate {
			return skipCandidate{start: start, end: end, source: source, priority: priority[source]}
		}
		var opCandidates, edCandidates, recapCandidates, previewCandidates []skipCandidate
		if aniskipRes != nil {
			opCandidates = append(opCandidates, candidate("AniSkip", aniskipRes.OpStart, aniskipRes.OpEnd))
			edCandidates = append(edCandidates, candidate("AniSkip", aniskipRes.EdStart, aniskipRes.EdEnd))
		}
		if skipdbRes != nil {
			opCandidates = append(opCandidates, candidate("SkipDB", skipdbRes.OpStart, skipdbRes.OpEnd))
			edCandidates = append(edCandidates, candidate("SkipDB", skipdbRes.EdStart, skipdbRes.EdEnd))
			recapCandidates = append(recapCandidates, candidate("SkipDB", skipdbRes.RecapStart, skipdbRes.RecapEnd))
			previewCandidates = append(previewCandidates, candidate("SkipDB", skipdbRes.PreviewStart, skipdbRes.PreviewEnd))
		}
		if introdbRes != nil {
			opCandidates = append(opCandidates, candidate("IntroDB", introdbRes.OpStart, introdbRes.OpEnd))
			edCandidates = append(edCandidates, candidate("IntroDB", introdbRes.EdStart, introdbRes.EdEnd))
			recapCandidates = append(recapCandidates, candidate("IntroDB", introdbRes.RecapStart, introdbRes.RecapEnd))
			previewCandidates = append(previewCandidates, candidate("IntroDB", introdbRes.PreviewStart, introdbRes.PreviewEnd))
		}
		if isAnime && askipTimes != nil {
			opCandidates = append(opCandidates, candidate("Anime-Skip", askipTimes.OpStart, askipTimes.OpEnd))
			edCandidates = append(edCandidates, candidate("Anime-Skip", askipTimes.EdStart, askipTimes.EdEnd))
			recapCandidates = append(recapCandidates, candidate("Anime-Skip", askipTimes.RecapStart, askipTimes.RecapEnd))
			previewCandidates = append(previewCandidates, candidate("Anime-Skip", askipTimes.PreviewStart, askipTimes.PreviewEnd))
		}
		times.OpStart, times.OpEnd, times.OpSource, times.OpAgreement, times.OpConflict = selectZone(opCandidates...)
		times.EdStart, times.EdEnd, times.EdSource, times.EdAgreement, times.EdConflict = selectZone(edCandidates...)
		times.RecapStart, times.RecapEnd, times.RecapSource, times.RecapAgree, times.RecapConflict = selectZone(recapCandidates...)
		times.PreviewStart, times.PreviewEnd, times.PrevSource, times.PrevAgree, times.PrevConflict = selectZone(previewCandidates...)
	}

	switch providerMode {
	case "anime-skip":
		applyAnimeSkip()
	case "aniskip":
		applyAniSkip()
	case "skipdb":
		applySkipDB()
	case "introdb":
		applyIntroDB()
	default: // "hybrid"
		applyConsensus()
	}

	if times.OpStart < 0 && times.EdStart < 0 && times.RecapStart < 0 && times.PreviewStart < 0 {
		if lookupFailed.Load() || ctx.Err() != nil {
			skipLog.Debug("skip lookup incomplete; not caching empty result", "title", media.SeriesTitle, "episode", media.EpisodeNumber, "imdb", imdbID, "err", ctx.Err())
			return nil, ""
		}
		cache.Set(cacheKey, times)
		skipLog.Debug("no skip intervals found", "title", media.SeriesTitle, "episode", media.EpisodeNumber, "imdb", imdbID)
		return nil, ""
	}

	cache.Set(cacheKey, times)
	return buildSkipArgsFromTimes(times, settings)
}

func skipCacheKey(media model.ResolvedMedia, providerMode string) string {
	return fmt.Sprintf("%d:%d:%d:%s:%s:%s:%s",
		media.TMDBID,
		media.SeasonNumber,
		media.EpisodeNumber,
		strings.TrimSpace(media.SeriesTitle),
		strings.TrimSpace(media.SeriesURL),
		media.MediaType,
		providerMode,
	)
}

func buildSkipArgsFromTimes(times combinedSkipTimes, settings SkipSettings) ([]string, string) {
	cleanupStaleScripts()
	if times.PreviewStart >= 0 && times.EdStart >= 0 && times.EdStart < times.PreviewStart && times.EdEnd > times.PreviewStart {
		times.EdEnd = times.PreviewStart
	}
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("kari-skip-%d-%d.lua", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(scriptPath, []byte(skipLuaScript), 0644); err != nil {
		skipLog.Debug("temp lua script write failed", "err", err)
		return nil, ""
	}
	autoIntro := 0
	if settings.AutoSkipIntro {
		autoIntro = 1
	}
	autoEnding := 0
	if settings.AutoSkipEnding {
		autoEnding = 1
	}
	autoRecap := 0
	if settings.SkipRecap {
		autoRecap = 1
	}
	autoPreview := 0
	if settings.SkipPreview {
		autoPreview = 1
	}

	liveFlag := 0
	if times.IsLive {
		liveFlag = 1
	}
	args := []string{
		fmt.Sprintf("--script=%s", scriptPath),
		fmt.Sprintf(
			"--script-opts=skip-recap_start=%f,skip-recap_end=%f,skip-op_start=%f,skip-op_end=%f,skip-ed_start=%f,skip-ed_end=%f,skip-preview_start=%f,skip-preview_end=%f,skip-auto_recap=%d,skip-auto_intro=%d,skip-auto_ending=%d,skip-auto_preview=%d,skip-min_duration=60.000000,skip-is_live=%d",
			times.RecapStart, times.RecapEnd,
			times.OpStart, times.OpEnd,
			times.EdStart, times.EdEnd,
			times.PreviewStart, times.PreviewEnd,
			autoRecap, autoIntro, autoEnding, autoPreview,
			liveFlag,
		),
	}
	return args, scriptPath
}

// cleanupSkipScript removes the temporary lua script.
func cleanupSkipScript(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

func cleanupStaleScripts() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "kari-skip-*.lua"))
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-1 * time.Hour)
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}
