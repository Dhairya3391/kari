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
        table.insert(segments, { at = opts.preview_start, label = "Preview" })
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
    if opts.ed_start >= 0 and time >= opts.ed_start and time < (opts.ed_end - 0.5) then
        return "ending", opts.ed_end
    end
    if opts.preview_start >= 0 and time >= opts.preview_start and time < (opts.preview_end - 0.5) then
        return "preview", opts.preview_end
    end
    return nil, nil
end

local zone_labels = {
    recap   = "Recap",
    intro   = "Opening",
    ending  = "Ending",
    preview = "Preview",
}

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
            local label = zone_labels[zone]

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
            mp.osd_message(zone_labels[zone] .. " Skipped", 2)
        end
    end)
end

pcall(function()
    mp.add_forced_key_binding("ENTER", "kari-skip", do_skip)
end)
`

// combinedSkipTimes holds unified intervals across providers.
type combinedSkipTimes struct {
	OpStart      float64
	OpEnd        float64
	EdStart      float64
	EdEnd        float64
	RecapStart   float64
	RecapEnd     float64
	PreviewStart float64
	PreviewEnd   float64
}

var (
	imdbRegex = regexp.MustCompile(`(?i)\b(tt\d{7,10})\b`)
	imdbCache = util.NewBoundedCache[string](500)
)

func resolveIMDbID(ctx context.Context, media model.ResolvedMedia, keyPool *tmdb.KeyPool, httpClient *http.Client) string {
	// 1. Direct regex match on URLs or titles
	for _, candidate := range []string{media.SeriesURL, media.EpisodeURL, media.MediaURL, media.SeriesTitle} {
		if m := imdbRegex.FindString(candidate); m != "" {
			return strings.ToLower(m)
		}
	}

	if httpClient == nil {
		httpClient = httpclient.NewWithTimeout(5 * time.Second)
	}

	// 2. Resolve from TMDBID if available
	if media.TMDBID > 0 && keyPool != nil {
		cacheKey := fmt.Sprintf("tmdb:%d:%s", media.TMDBID, media.MediaType)
		if cached, ok := imdbCache.Get(cacheKey); ok {
			return cached
		}

		apiKey, err := keyPool.NextKey()
		if err == nil && apiKey != "" {
			mediaType := "tv"
			if media.MediaType == "movie" || (media.SeasonNumber == 0 && media.EpisodeNumber <= 1) {
				mediaType = "movie"
			}
			endpoint := fmt.Sprintf("%s/%s/%d/external_ids?api_key=%s",
				config.TMDBAPIBase, mediaType, media.TMDBID, url.QueryEscape(apiKey))
			req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if rerr == nil {
				req.Header.Set("Accept", "application/json")
				req.Header.Set("User-Agent", config.DesktopUserAgent)
				resp, derr := httpClient.Do(req)
				if derr == nil {
					defer resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						var res struct {
							IMDbID string `json:"imdb_id"`
						}
						if jerr := json.NewDecoder(resp.Body).Decode(&res); jerr == nil && strings.TrimSpace(res.IMDbID) != "" {
							id := strings.ToLower(strings.TrimSpace(res.IMDbID))
							imdbCache.Set(cacheKey, id)
							return id
						}
					}
				}
			}
		}
	}

	// 3. Fallback: Search TMDB by title if TMDBID is not populated
	title := strings.TrimSpace(media.SeriesTitle)
	if title != "" && keyPool != nil {
		cacheKey := fmt.Sprintf("title:%s:%s", title, media.MediaType)
		if cached, ok := imdbCache.Get(cacheKey); ok {
			return cached
		}

		apiKey, err := keyPool.NextKey()
		if err == nil && apiKey != "" {
			endpoint := fmt.Sprintf("%s/search/multi?query=%s&api_key=%s",
				config.TMDBAPIBase, url.QueryEscape(title), url.QueryEscape(apiKey))
			req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if rerr == nil {
				req.Header.Set("Accept", "application/json")
				req.Header.Set("User-Agent", config.DesktopUserAgent)
				resp, derr := httpClient.Do(req)
				if derr == nil {
					defer resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						var searchRes struct {
							Results []struct {
								ID        int    `json:"id"`
								MediaType string `json:"media_type"`
							} `json:"results"`
						}
						if jerr := json.NewDecoder(resp.Body).Decode(&searchRes); jerr == nil && len(searchRes.Results) > 0 {
							first := searchRes.Results[0]
							mType := first.MediaType
							if mType != "movie" && mType != "tv" {
								mType = "tv"
							}
							extEndpoint := fmt.Sprintf("%s/%s/%d/external_ids?api_key=%s",
								config.TMDBAPIBase, mType, first.ID, url.QueryEscape(apiKey))
							extReq, ererr := http.NewRequestWithContext(ctx, http.MethodGet, extEndpoint, nil)
							if ererr == nil {
								extReq.Header.Set("Accept", "application/json")
								extReq.Header.Set("User-Agent", config.DesktopUserAgent)
								extResp, ederr := httpClient.Do(extReq)
								if ederr == nil {
									defer extResp.Body.Close()
									if extResp.StatusCode == http.StatusOK {
										var extRes struct {
											IMDbID string `json:"imdb_id"`
										}
										if ejerr := json.NewDecoder(extResp.Body).Decode(&extRes); ejerr == nil && strings.TrimSpace(extRes.IMDbID) != "" {
											id := strings.ToLower(strings.TrimSpace(extRes.IMDbID))
											imdbCache.Set(cacheKey, id)
											return id
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return ""
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

	cacheKey := fmt.Sprintf(
		"%d:%d:%d:%s:%s:%s:%s",
		media.TMDBID,
		media.SeasonNumber,
		media.EpisodeNumber,
		strings.TrimSpace(media.SeriesTitle),
		strings.TrimSpace(media.SeriesURL),
		media.MediaType,
		providerMode,
	)
	if cached, ok := cache.Get(cacheKey); ok {
		return buildSkipArgsFromTimes(cached, settings)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	// 1. Resolve AniList ID from media metadata if available
	var anilistID int
	if trimmed := strings.Trim(strings.TrimSpace(media.SeriesURL), "/"); trimmed != "" {
		if id, err := strconv.Atoi(trimmed); err == nil && id > 0 {
			anilistID = id
		}
	}

	isMovie := media.MediaType == "movie" || (media.SeasonNumber == 0 && media.EpisodeNumber == 0 && media.MediaType != "tv" && media.MediaType != "anime")
	times := combinedSkipTimes{
		OpStart: -1, OpEnd: -1,
		EdStart: -1, EdEnd: -1,
		RecapStart: -1, RecapEnd: -1,
		PreviewStart: -1, PreviewEnd: -1,
	}

	var (
		g          errgroup.Group
		imdbID     string
		askipTimes *animeskip.SkipTimes
		aniskipRes *aniskip.SkipTimes
		skipdbRes  *skipdb.SkipTimes
		introdbRes *introdb.SkipTimes
	)

	// Anime-Skip
	if (providerMode == "hybrid" || providerMode == "anime-skip") && clients.AnimeSkip != nil && !isMovie {
		g.Go(func() error {
			aniIDStr := ""
			if anilistID > 0 {
				aniIDStr = strconv.Itoa(anilistID)
			}
			t, err := clients.AnimeSkip.GetTimestamps(ctx, aniIDStr, media.EpisodeNumber, media.SeriesTitle, media.EpisodeTitle)
			if err != nil {
				skipLog.Debug("anime-skip lookup error", "err", err)
			} else {
				askipTimes = t
			}
			return nil
		})
	}

	// AniSkip
	if (providerMode == "hybrid" || providerMode == "aniskip") && clients.AniSkip != nil && !isMovie {
		g.Go(func() error {
			malID := 0
			if anilistID > 0 {
				_, mID, err := clients.AniSkip.GetIDs(ctx, media.SeriesTitle)
				if err == nil {
					malID = mID
				}
			} else if strings.TrimSpace(media.SeriesTitle) != "" {
				_, mID, err := clients.AniSkip.GetIDs(ctx, media.SeriesTitle)
				if err == nil {
					malID = mID
				}
			}
			if malID > 0 {
				t, err := clients.AniSkip.GetSkipTimes(ctx, malID, media.EpisodeNumber)
				if err != nil {
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
			id := resolveIMDbID(ctx, media, clients.TMDB, clients.HTTP)
			if id == "" {
				return nil
			}
			imdbID = id
			var subG errgroup.Group
			if (providerMode == "hybrid" || providerMode == "skipdb") && clients.SkipDB != nil {
				subG.Go(func() error {
					t, err := clients.SkipDB.GetSegments(ctx, imdbID, media.SeasonNumber, media.EpisodeNumber, isMovie)
					if err != nil {
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

	isAnime := media.MediaType == "anime" || anilistID > 0

	applyAnimeSkip := func() {
		if askipTimes != nil {
			if times.OpStart < 0 && askipTimes.OpStart >= 0 {
				times.OpStart, times.OpEnd = askipTimes.OpStart, askipTimes.OpEnd
			}
			if times.EdStart < 0 && askipTimes.EdStart >= 0 {
				times.EdStart, times.EdEnd = askipTimes.EdStart, askipTimes.EdEnd
			}
			if times.RecapStart < 0 && askipTimes.RecapStart >= 0 {
				times.RecapStart, times.RecapEnd = askipTimes.RecapStart, askipTimes.RecapEnd
			}
			if times.PreviewStart < 0 && askipTimes.PreviewStart >= 0 {
				times.PreviewStart, times.PreviewEnd = askipTimes.PreviewStart, askipTimes.PreviewEnd
			}
		}
	}

	applyAniSkip := func() {
		if aniskipRes != nil {
			if times.OpStart < 0 && aniskipRes.OpStart >= 0 {
				times.OpStart, times.OpEnd = aniskipRes.OpStart, aniskipRes.OpEnd
			}
			if times.EdStart < 0 && aniskipRes.EdStart >= 0 {
				times.EdStart, times.EdEnd = aniskipRes.EdStart, aniskipRes.EdEnd
			}
		}
	}

	applySkipDB := func() {
		if skipdbRes != nil {
			if times.OpStart < 0 && skipdbRes.OpStart >= 0 {
				times.OpStart, times.OpEnd = skipdbRes.OpStart, skipdbRes.OpEnd
			}
			if times.EdStart < 0 && skipdbRes.EdStart >= 0 {
				times.EdStart, times.EdEnd = skipdbRes.EdStart, skipdbRes.EdEnd
			}
			if times.RecapStart < 0 && skipdbRes.RecapStart >= 0 {
				times.RecapStart, times.RecapEnd = skipdbRes.RecapStart, skipdbRes.RecapEnd
			}
			if times.PreviewStart < 0 && skipdbRes.PreviewStart >= 0 {
				times.PreviewStart, times.PreviewEnd = skipdbRes.PreviewStart, skipdbRes.PreviewEnd
			}
		}
	}

	applyIntroDB := func() {
		if introdbRes != nil {
			if times.OpStart < 0 && introdbRes.OpStart >= 0 {
				times.OpStart, times.OpEnd = introdbRes.OpStart, introdbRes.OpEnd
			}
			if times.EdStart < 0 && introdbRes.EdStart >= 0 {
				times.EdStart, times.EdEnd = introdbRes.EdStart, introdbRes.EdEnd
			}
			if times.RecapStart < 0 && introdbRes.RecapStart >= 0 {
				times.RecapStart, times.RecapEnd = introdbRes.RecapStart, introdbRes.RecapEnd
			}
			if times.PreviewStart < 0 && introdbRes.PreviewStart >= 0 {
				times.PreviewStart, times.PreviewEnd = introdbRes.PreviewStart, introdbRes.PreviewEnd
			}
		}
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
		if isAnime {
			applyAnimeSkip()
			applyAniSkip()
			applySkipDB()
			applyIntroDB()
		} else {
			applySkipDB()
			applyIntroDB()
			applyAnimeSkip()
			applyAniSkip()
		}
	}

	if times.OpStart < 0 && times.EdStart < 0 && times.RecapStart < 0 && times.PreviewStart < 0 {
		skipLog.Debug("no skip intervals found", "title", media.SeriesTitle, "episode", media.EpisodeNumber, "imdb", imdbID)
		return nil, ""
	}

	cache.Set(cacheKey, times)
	return buildSkipArgsFromTimes(times, settings)
}

func buildSkipArgsFromTimes(times combinedSkipTimes, settings SkipSettings) ([]string, string) {
	cleanupStaleScripts()

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

	args := []string{
		fmt.Sprintf("--script=%s", scriptPath),
		fmt.Sprintf(
			"--script-opts=skip-recap_start=%f,skip-recap_end=%f,skip-op_start=%f,skip-op_end=%f,skip-ed_start=%f,skip-ed_end=%f,skip-preview_start=%f,skip-preview_end=%f,skip-auto_recap=%d,skip-auto_intro=%d,skip-auto_ending=%d,skip-auto_preview=%d",
			times.RecapStart, times.RecapEnd,
			times.OpStart, times.OpEnd,
			times.EdStart, times.EdEnd,
			times.PreviewStart, times.PreviewEnd,
			autoRecap, autoIntro, autoEnding, autoPreview,
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
