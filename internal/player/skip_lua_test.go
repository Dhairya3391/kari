package player

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// luaTestRunner runs a Lua test script against a mock MPV environment
// executing the actual skipLuaScript constant.
func runLuaTestScript(t *testing.T, optsSetup, testBody string) string {
	t.Helper()
	luaBin, err := exec.LookPath("luajit")
	if err != nil {
		luaBin, err = exec.LookPath("lua")
		if err != nil {
			t.Skip("neither luajit nor lua found in PATH; skipping Lua script execution test")
		}
	}

	harness := fmt.Sprintf(`
local mp = {
    events = {},
    observed = {},
    bindings = {},
    properties = {},
    commands = {},
    osd = {}
}

package.loaded["mp"] = mp
package.loaded["mp.options"] = {
    read_options = function(target, name)
        %s
    end
}

_G.mp = mp
mp.register_event = function(name, fn) table.insert(mp.events, {name = name, fn = fn}) end
mp.observe_property = function(name, typ, fn) mp.observed[name] = fn end
mp.add_forced_key_binding = function(key, id, fn) mp.bindings[key] = fn end
mp.commandv = function(...) table.insert(mp.commands, {...}) end
mp.osd_message = function(msg, dur) table.insert(mp.osd, {msg = msg, dur = dur}) end
mp.get_property_number = function(name) return mp.properties[name] end
mp.get_property = function(name) return mp.properties[name] end
mp.get_property_native = function(name) return mp.properties[name] end
mp.set_property_native = function(name, val) mp.properties[name] = val end

-- Load skipLuaScript
%s

-- Run test body
%s
`, optsSetup, skipLuaScript, testBody)

	cmd := exec.Command(luaBin, "-e", harness)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Lua execution failed: %v\nStderr: %s\nStdout: %s", err, stderr.String(), stdout.String())
	}
	return strings.TrimSpace(stdout.String())
}

func TestLuaScript_PostCreditsPromptWhenSetToAsk(t *testing.T) {
	opts := `
        target.ed_start = 1200
        target.ed_end = 1240
        target.preview_start = 1240
        target.preview_end = 1300
        target.auto_ending = 1
        target.auto_preview = 0
`
	test := `
        -- 1. At t=1200 (ending), auto-ending fires and seeks to 1240
        mp.observed["time-pos"](nil, 1200)
        assert(#mp.commands == 1, "expected 1 seek command for ending")
        assert(mp.commands[1][1] == "seek" and mp.commands[1][2] == 1240, "seeked to: " .. tostring(mp.commands[1][2]))
        assert(#mp.osd == 1 and mp.osd[1].msg == "Ending Skipped", "osd: " .. tostring(mp.osd[1] and mp.osd[1].msg))

        -- 2. At t=1240 (post-credits start), auto_preview=0 (ask) MUST NOT auto-seek!
        mp.commands = {}
        mp.osd = {}
        mp.observed["time-pos"](nil, 1240)
        assert(#mp.commands == 0, "auto_preview is 0: must not auto-seek during post-credits!")
        assert(#mp.osd == 1, "expected prompt for post-credits")
        assert(mp.osd[1].msg == "Press 'Enter' to Skip Post-Credits", "got prompt: " .. tostring(mp.osd[1].msg))

        -- 3. Inside post-credits at t=1260: prompt continues, no auto-seek
        mp.observed["time-pos"](nil, 1260)
        assert(#mp.commands == 0, "must still not auto-seek")

        print("OK_POST_CREDITS_PROMPT")
`
	out := runLuaTestScript(t, opts, test)
	if !strings.Contains(out, "OK_POST_CREDITS_PROMPT") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestLuaScript_PostCreditsManualSkip(t *testing.T) {
	opts := `
        target.ed_start = 1200
        target.ed_end = 1240
        target.preview_start = 1240
        target.preview_end = 1300
        target.auto_ending = 1
        target.auto_preview = 0
`
	test := `
        mp.properties["time-pos"] = 1250
        mp.observed["time-pos"](nil, 1250)
        mp.commands = {}
        mp.osd = {}

        -- User presses ENTER
        mp.bindings["ENTER"]()
        assert(#mp.commands == 1, "expected seek command on ENTER")
        assert(mp.commands[1][1] == "seek" and mp.commands[1][2] == 1300, "seeked to: " .. tostring(mp.commands[1][2]))
        assert(#mp.osd == 1 and mp.osd[1].msg == "Post-Credits Skipped", "osd: " .. tostring(mp.osd[1] and mp.osd[1].msg))

        print("OK_MANUAL_SKIP")
`
	out := runLuaTestScript(t, opts, test)
	if !strings.Contains(out, "OK_MANUAL_SKIP") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestLuaScript_PostCreditsAutoSkipWhenSetToAuto(t *testing.T) {
	opts := `
        target.ed_start = 1200
        target.ed_end = 1240
        target.preview_start = 1240
        target.preview_end = 1300
        target.auto_ending = 1
        target.auto_preview = 1
`
	test := `
        mp.observed["time-pos"](nil, 1245)
        assert(#mp.commands == 1, "expected auto-seek command")
        assert(mp.commands[1][1] == "seek" and mp.commands[1][2] == 1300, "seeked to: " .. tostring(mp.commands[1][2]))
        assert(#mp.osd == 1 and mp.osd[1].msg == "Post-Credits Skipped", "osd: " .. tostring(mp.osd[1] and mp.osd[1].msg))

        print("OK_AUTO_SKIP_POST_CREDITS")
`
	out := runLuaTestScript(t, opts, test)
	if !strings.Contains(out, "OK_AUTO_SKIP_POST_CREDITS") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestLuaScript_OutroAskPromptAndManualSkip(t *testing.T) {
	opts := `
        target.ed_start = 1200
        target.ed_end = 1240
        target.auto_ending = 0
`
	test := `
        mp.observed["time-pos"](nil, 1210)
        assert(#mp.commands == 0, "auto_ending is 0: must not auto-seek")
        assert(#mp.osd == 1 and mp.osd[1].msg == "Press 'Enter' to Skip Ending", "prompt: " .. tostring(mp.osd[1] and mp.osd[1].msg))

        mp.properties["time-pos"] = 1210
        mp.bindings["ENTER"]()
        assert(#mp.commands == 1 and mp.commands[1][2] == 1240, "seek on enter to ed_end")
        assert(#mp.osd == 2 and mp.osd[2].msg == "Ending Skipped", "osd: " .. tostring(mp.osd[2] and mp.osd[2].msg))

        print("OK_OUTRO_ASK")
`
	out := runLuaTestScript(t, opts, test)
	if !strings.Contains(out, "OK_OUTRO_ASK") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestLuaScript_ChapterInjectionWithPostCredits(t *testing.T) {
	opts := `
        target.op_start = 60
        target.op_end = 150
        target.ed_start = 1200
        target.ed_end = 1240
        target.preview_start = 1240
        target.preview_end = 1300
`
	test := `
        for _, ev in ipairs(mp.events) do
            if ev.name == "file-loaded" then
                ev.fn()
            end
        end
        local chapters = mp.properties["chapter-list"]
        assert(chapters ~= nil and #chapters > 0, "expected chapters to be set")

        local found_op = false
        local found_ed = false
        local found_pc = false
        for _, ch in ipairs(chapters) do
            if ch.title == "Opening" and ch.time == 60 then found_op = true end
            if ch.title == "Ending" and ch.time == 1200 then found_ed = true end
            if ch.title == "Post-Credits" and ch.time == 1240 then found_pc = true end
        end

        assert(found_op, "missing Opening chapter")
        assert(found_ed, "missing Ending chapter")
        assert(found_pc, "missing Post-Credits chapter")

        print("OK_CHAPTER_INJECTION")
`
	out := runLuaTestScript(t, opts, test)
	if !strings.Contains(out, "OK_CHAPTER_INJECTION") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestLuaScript_FiltersShortWarningVideo(t *testing.T) {
	opts := `
        target.min_duration = 60
        target.is_live = 0
`
	test := `
        -- 1. Remote 45-second stream (fake/warning video) must be quit immediately
        mp.properties["path"] = "https://cdn.example.com/warning.m3u8"
        mp.observed["duration"](nil, 45)
        assert(#mp.commands == 1, "expected quit command for 45s warning video, got: " .. #mp.commands)
        assert(mp.commands[1][1] == "quit" and mp.commands[1][2] == "2", "expected quit 2")
        assert(#mp.osd == 1 and mp.osd[1].msg == "Filtering short warning video (45s)...", "osd: " .. tostring(mp.osd[1] and mp.osd[1].msg))

        -- 2. Real episode (1440s) must NOT be filtered
        mp.commands = {}
        mp.osd = {}
        mp.properties["path"] = "https://cdn.example.com/real_episode.m3u8"
        mp.observed["duration"](nil, 1440)
        assert(#mp.commands == 0, "must not issue quit command for 1440s real episode")

        -- 3. Local/synthetic av:// test stream must NOT be filtered
        mp.commands = {}
        mp.osd = {}
        mp.properties["path"] = "av://lavfi:testsrc"
        mp.observed["duration"](nil, 1.0)
        assert(#mp.commands == 0, "must not issue quit command for av:// test source")


        -- 4. Live stream with duration <= 45s must NOT be filtered
        mp.commands = {}
        mp.osd = {}
        package.loaded["mp.options"].read_options(opts, "skip")
        -- Directly test opts.is_live == 1
        opts.is_live = 1
        mp.properties["path"] = "https://cdn.example.com/live.m3u8"
        mp.observed["duration"](nil, 45)
        assert(#mp.commands == 0, "must not issue quit command for live stream")
        print("OK_SHORT_WARNING_FILTER")
`
	out := runLuaTestScript(t, opts, test)
	if !strings.Contains(out, "OK_SHORT_WARNING_FILTER") {
		t.Errorf("unexpected output: %s", out)
	}
}
