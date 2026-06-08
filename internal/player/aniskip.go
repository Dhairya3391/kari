package player

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kari/internal/aniskip"
	"kari/internal/logging"
	"kari/internal/model"
)

const aniskipLuaScript = `
local opts = {
    op_start = -1,
    op_end = -1,
    ed_start = -1,
    ed_end = -1,
}

require 'mp.options'.read_options(opts, "skip")

local in_op = false
local in_ed = false

local function do_skip()
    if in_op then
        mp.commandv("seek", opts.op_end, "absolute")
        mp.osd_message("Opening Skipped", 3)
    elseif in_ed then
        mp.commandv("seek", opts.ed_end, "absolute")
        mp.osd_message("Ending Skipped", 3)
    end
end

mp.add_forced_key_binding("ENTER", "skip-intro-outro", do_skip)

mp.observe_property("time-pos", "number", function(name, time)
    if not time then return end
    
    local op_margin = (opts.op_end - opts.op_start > 2.0) and 1.0 or 0.0
    local ed_margin = (opts.ed_end - opts.ed_start > 2.0) and 1.0 or 0.0
    
    local new_in_op = (opts.op_start >= 0 and opts.op_end >= 0 and time >= opts.op_start and time < (opts.op_end - op_margin))
    local new_in_ed = (opts.ed_start >= 0 and opts.ed_end >= 0 and time >= opts.ed_start and time < (opts.ed_end - ed_margin))
    
    -- Show the prompt continuously while in the interval so it isn't cleared by buffering
    if new_in_op then
        mp.osd_message("Press 'Enter' to Skip Opening", 1)
    elseif new_in_ed then
        mp.osd_message("Press 'Enter' to Skip Ending", 1)
    end
    
    in_op = new_in_op
    in_ed = new_in_ed
end)
`

// getAniskipArgs fetches skip times and creates a temporary lua script, returning the MPV arguments needed.
func getAniskipArgs(client *aniskip.Client, media model.ResolvedMedia) ([]string, string) {
	if client == nil {
		logging.Debugf("aniskip: client is nil, skipping")
		return nil, ""
	}
	if media.EpisodeNumber <= 0 || media.SeriesTitle == "" {
		return nil, ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	malID, err := client.GetMALID(ctx, media.SeriesTitle)
	if err != nil {
		logging.Debugf("aniskip: failed to get MAL ID: %v", err)
		return nil, ""
	}

	times, err := client.GetSkipTimes(ctx, malID, media.EpisodeNumber)
	if err != nil {
		logging.Debugf("aniskip: failed to get skip times: %v", err)
		return nil, ""
	}
	if times == nil {
		logging.Debugf("aniskip: no skip times found for mal_id %d ep %d", malID, media.EpisodeNumber)
		return nil, ""
	}

	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("kari-skip-%d-%d.lua", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(scriptPath, []byte(aniskipLuaScript), 0644); err != nil {
		logging.Debugf("aniskip: failed to write temp lua script: %v", err)
		return nil, ""
	}

	args := []string{
		fmt.Sprintf("--script=%s", scriptPath),
		fmt.Sprintf("--script-opts=skip-op_start=%f,skip-op_end=%f,skip-ed_start=%f,skip-ed_end=%f",
			times.OpStart, times.OpEnd, times.EdStart, times.EdEnd),
	}

	return args, scriptPath
}

// cleanupAniskipScript removes the temporary lua script.
func cleanupAniskipScript(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
