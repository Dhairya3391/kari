package player

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kari/internal/animeskip"
	"kari/internal/aniskip"
	"kari/internal/logging"
	"kari/internal/model"
)

// log scopes every line from this package/component.
var skipLog = logging.With("component", "player.skip")

// SkipSettings holds the configured skip provider and per-zone auto-skip flags.
type SkipSettings struct {
	Provider       string // "hybrid", "anime-skip", "aniskip", "off"
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

require 'mp.options'.read_options(opts, "skip")

-- ── Chapter injection ────────────────────────────────────────────────────────

local function build_chapters()
    local chapters = {}
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

    table.sort(segments, function(a, b)
        if math.abs(a.at - b.at) > 0.1 then
            return a.at < b.at
        end
        if a.label ~= "Episode" and b.label == "Episode" then
            return true
        end
        return false
    end)

    if #segments == 0 then
        return chapters
    end

    if segments[1].at > 0.5 then
        table.insert(chapters, { title = "Episode", time = 0 })
    end

    local last_time = -1
    local last_label = nil
    for _, seg in ipairs(segments) do
        if seg.label ~= last_label and (last_time < 0 or (seg.at - last_time) >= 0.5) then
            table.insert(chapters, { title = seg.label, time = seg.at })
            last_time = seg.at
            last_label = seg.label
        elseif seg.label ~= "Episode" and last_label == "Episode" and (seg.at - last_time) < 0.5 then
            if #chapters > 0 then
                chapters[#chapters] = { title = seg.label, time = seg.at }
                last_label = seg.label
            end
        end
    end
    return chapters
end

local function inject_chapters()
    local chapters = build_chapters()
    if #chapters == 0 then
        return
    end
    mp.set_property_native("chapter-list", chapters)
end

mp.register_event("file-loaded", inject_chapters)

-- ── Zone detection ───────────────────────────────────────────────────────────

local function active_zone(time)
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

mp.observe_property("time-pos", "number", function(_, time)
    if not time then return end

    local zone, end_time = active_zone(time)

    if zone then
        local label = zone_labels[zone]

        if auto_flags[zone]() and not auto_fired[zone] then
            auto_fired[zone] = true
            mp.commandv("seek", end_time, "absolute")
            mp.osd_message(label .. " Skipped", 2)
            last_zone = nil
            return
        end

        if zone ~= last_zone then
            for z in pairs(auto_fired) do
                if z ~= zone then auto_fired[z] = nil end
            end
        end

        mp.osd_message("Press 'Enter' to Skip " .. label, 1)
        last_zone = zone
    else
        if last_zone then
            auto_fired[last_zone] = nil
        end
        last_zone = nil
    end
end)

-- ── ENTER key binding ────────────────────────────────────────────────────────

local function do_skip()
    local time = mp.get_property_number("time-pos")
    if not time then return end

    local zone, end_time = active_zone(time)
    if zone then
        mp.commandv("seek", end_time, "absolute")
        mp.osd_message(zone_labels[zone] .. " Skipped", 2)
    end
end

mp.add_forced_key_binding("ENTER", "kari-skip", do_skip)
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

// getSkipArgs resolves skip intervals according to settings and writes a
// temporary Lua script for MPV. Returns MPV arguments and script path.
func getSkipArgs(
	aniskipClient *aniskip.Client,
	animeskipClient *animeskip.Client,
	settings SkipSettings,
	media model.ResolvedMedia,
) ([]string, string) {
	providerMode := strings.ToLower(strings.TrimSpace(settings.Provider))
	if providerMode == "" {
		providerMode = "hybrid"
	}
	if providerMode == "off" || providerMode == "none" {
		return nil, ""
	}
	if media.EpisodeNumber <= 0 || media.SeriesTitle == "" {
		return nil, ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Resolve IDs
	var anilistID, malID int
	if aniskipClient != nil {
		// Check if SeriesURL is an all-numeric AniList ID (from Miruro).
		// Trim surrounding slashes/spaces to handle IDs like "12345/".
		if trimmed := strings.Trim(strings.TrimSpace(media.SeriesURL), "/"); trimmed != "" {
			if _, err := strconv.Atoi(trimmed); err == nil {
				anilistID, _ = strconv.Atoi(trimmed)
			}
		}
		// Query AniList for missing IDs
		foundAniListID, foundMALID, err := aniskipClient.GetIDs(ctx, media.SeriesTitle)
		if err == nil {
			if anilistID == 0 {
				anilistID = foundAniListID
			}
			malID = foundMALID
		} else {
			skipLog.Debug("anilist lookup failed", "title", media.SeriesTitle, "err", err)
		}
	}

	times := combinedSkipTimes{
		OpStart: -1, OpEnd: -1,
		EdStart: -1, EdEnd: -1,
		RecapStart: -1, RecapEnd: -1,
		PreviewStart: -1, PreviewEnd: -1,
	}

	// 2. Query Anime-Skip if provider is "hybrid" or "anime-skip"
	if (providerMode == "hybrid" || providerMode == "anime-skip") && animeskipClient != nil {
		aniIDStr := ""
		if anilistID > 0 {
			aniIDStr = strconv.Itoa(anilistID)
		}
		askipTimes, err := animeskipClient.GetTimestamps(ctx, aniIDStr, media.EpisodeNumber, media.SeriesTitle, media.EpisodeTitle)
		if err != nil {
			skipLog.Debug("anime-skip lookup error", "err", err)
		} else if askipTimes != nil {
			times.OpStart = askipTimes.OpStart
			times.OpEnd = askipTimes.OpEnd
			times.EdStart = askipTimes.EdStart
			times.EdEnd = askipTimes.EdEnd
			times.RecapStart = askipTimes.RecapStart
			times.RecapEnd = askipTimes.RecapEnd
			times.PreviewStart = askipTimes.PreviewStart
			times.PreviewEnd = askipTimes.PreviewEnd
		}
	}

	// 3. Query AniSkip if provider is "aniskip" or ("hybrid" gap-filling missing OP/ED)
	if (providerMode == "aniskip" || (providerMode == "hybrid" && (times.OpStart < 0 || times.EdStart < 0))) && aniskipClient != nil && malID > 0 {
		aniskipRes, err := aniskipClient.GetSkipTimes(ctx, malID, media.EpisodeNumber)
		if err != nil {
			skipLog.Debug("aniskip lookup error", "err", err)
		} else if aniskipRes != nil {
			if times.OpStart < 0 && aniskipRes.OpStart >= 0 {
				times.OpStart = aniskipRes.OpStart
				times.OpEnd = aniskipRes.OpEnd
			}
			if times.EdStart < 0 && aniskipRes.EdStart >= 0 {
				times.EdStart = aniskipRes.EdStart
				times.EdEnd = aniskipRes.EdEnd
			}
		}
	}

	// If no intervals were found at all, don't generate a script
	if times.OpStart < 0 && times.EdStart < 0 && times.RecapStart < 0 && times.PreviewStart < 0 {
		skipLog.Debug("no skip intervals found", "title", media.SeriesTitle, "episode", media.EpisodeNumber)
		return nil, ""
	}

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

// cleanupAniskipScript removes the temporary lua script.
func cleanupAniskipScript(path string) {
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
