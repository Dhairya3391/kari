//go:build !android

package player

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"kari/internal/model"
	"kari/internal/provider"
)

func testSelectedSubtitle(path string) *model.SubtitleTrack {
	return &model.SubtitleTrack{Path: path, Language: "en"}
}

type stubPlayer struct {
	name      string
	available bool
	play      func() (PlaybackResult, error)
}

func (p *stubPlayer) Name() string    { return p.name }
func (p *stubPlayer) Available() bool { return p.available }
func (p *stubPlayer) Play([]provider.MediaSource, model.ResolvedMedia) (PlaybackResult, error) {
	return p.play()
}

func TestAppendAudioLangArgs(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"hi", "--alang=hi,hin,hindi,en,eng"},
		{"hindi", "--alang=hi,hin,hindi,en,eng"},
		{"ja", "--alang=ja,jpn,japanese,en,eng"},
		{"es", "--alang=es,spa,spanish,esla,es-la,en,eng"},
		{"", ""},
	}

	for _, tt := range tests {
		got := appendAudioLangArgs(nil, tt.lang)
		if tt.want == "" {
			if len(got) != 0 {
				t.Errorf("appendAudioLangArgs(nil, %q) = %v, want empty", tt.lang, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("appendAudioLangArgs(nil, %q) = %v, want %q", tt.lang, got, tt.want)
		}
	}
}

func TestBuildMPVArgsIncludesAlang(t *testing.T) {
	source := provider.MediaSource{
		URL:      "https://example.com/stream.m3u8",
		Language: "hi",
	}
	media := model.ResolvedMedia{
		SeriesTitle: "The Boys",
	}

	args := buildMPVArgs(source, media, "/tmp/sock", nil)

	foundAlang := false
	for _, a := range args {
		if strings.HasPrefix(a, "--alang=") {
			foundAlang = true
			if !strings.Contains(a, "hi") {
				t.Errorf("expected --alang to contain 'hi', got %q", a)
			}
		}
	}
	if !foundAlang {
		t.Errorf("buildMPVArgs missing --alang option: %v", args)
	}
}

func TestDesktopPlayersUseTheirNativeAudioLanguageOptions(t *testing.T) {
	source := provider.MediaSource{URL: "https://example.com/stream.m3u8", Language: "hi"}
	media := model.ResolvedMedia{}

	vlcArgs := buildVLCArgs(source, media)
	if !containsArg(vlcArgs, "--audio-language=hi") {
		t.Errorf("VLC args missing --audio-language: %v", vlcArgs)
	}
	if containsArg(vlcArgs, "--alang=hi") {
		t.Errorf("VLC args contain unsupported --alang: %v", vlcArgs)
	}

	iinaArgs := buildIINAArgs(source, media, "/tmp/iina.sock", nil)
	if !containsArg(iinaArgs, "--alang=hi,hin,hindi,en,eng") {
		t.Errorf("IINA args missing mpv --alang: %v", iinaArgs)
	}
}

func TestBuildIINAArgsUsesSubFilesOption(t *testing.T) {
	source := provider.MediaSource{URL: "https://example.com/stream.m3u8"}
	media := model.ResolvedMedia{
		SeriesTitle:      "Sintel",
		SelectedSubtitle: testSelectedSubtitle("/Users/test/.config/kari/subs/test.srt"),
	}

	args := buildIINAArgs(source, media, "/tmp/iina.sock", nil)
	if !containsArg(args, "--sub-files=/Users/test/.config/kari/subs/test.srt") {
		t.Errorf("IINA args missing --sub-files option: %v", args)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--sub-file=") || strings.HasPrefix(a, "--sub-files-append=") {
			t.Errorf("IINA args contain option IINA ignores via libmpv: %q (full args: %v)", a, args)
		}
	}
}

func TestBuildIINAArgsIncludesSkipArgs(t *testing.T) {
	source := provider.MediaSource{URL: "https://example.com/stream.m3u8"}
	media := model.ResolvedMedia{SeriesTitle: "Frieren"}
	skipArgs := []string{"--script=/tmp/skip.lua", "--script-opts=skip-op_start=10.000000"}

	args := buildIINAArgs(source, media, "/tmp/iina.sock", skipArgs)
	if !containsArg(args, "--script=/tmp/skip.lua") {
		t.Errorf("IINA args missing skip script: %v", args)
	}
	if !containsArg(args, "--script-opts=skip-op_start=10.000000") {
		t.Errorf("IINA args missing skip opts: %v", args)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func indexOf(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}

func TestBuildMPVArgsIncludesIPCServer(t *testing.T) {
	source := provider.MediaSource{
		URL: "https://example.com/stream.m3u8",
	}
	media := model.ResolvedMedia{
		SeriesTitle: "Inception",
	}

	socketPath := "/tmp/test-mpv.sock"
	args := buildMPVArgs(source, media, socketPath, nil)

	foundIPC := false
	for _, a := range args {
		if a == "--input-ipc-server="+socketPath {
			foundIPC = true
			break
		}
	}
	if !foundIPC {
		t.Errorf("buildMPVArgs missing --input-ipc-server option: %v", args)
	}
}

func TestBuildMPVArgsAutoDetectsHLSDemuxer(t *testing.T) {
	// mpv detects HLS by content; forcing --demuxer=lavf --demuxer-lavf-format=hls
	// at the player level caused glitched A/V on some streams. Providers with a
	// host-specific demuxer quirk opt in themselves via ExtraArgs.
	args := buildMPVArgs(provider.MediaSource{
		URL:  "https://example.com/stream.m3u8",
		Type: provider.SourceTypeHLS,
	}, model.ResolvedMedia{}, "/tmp/sock", nil)
	for _, a := range args {
		if strings.HasPrefix(a, "--demuxer=") || strings.HasPrefix(a, "--demuxer-lavf-format=") ||
			strings.HasPrefix(a, "--demuxer-lavf-analyzeduration=") || strings.HasPrefix(a, "--demuxer-lavf-probesize=") {
			t.Fatalf("player must not force demuxer/probing options, got %q in %v", a, args)
		}
	}
	if !containsArg(args, "--hls-bitrate=max") {
		t.Fatalf("HLS bitrate preference missing: %v", args)
	}
}

func TestBuildMPVArgsPassesSubtitlesRegardlessOfSubtype(t *testing.T) {
	for _, subType := range []string{provider.SubTypeHard, provider.SubTypeSoft, ""} {
		t.Run(subType, func(t *testing.T) {
			source := provider.MediaSource{URL: "https://example.com/stream.m3u8", SubType: subType}
			media := model.ResolvedMedia{SelectedSubtitle: testSelectedSubtitle("/tmp/subtitles.srt")}
			args := buildMPVArgs(source, media, "/tmp/sock", nil)
			if !containsArg(args, "--sub-file=/tmp/subtitles.srt") {
				t.Fatalf("subtitle argument missing for subtype %q: %v", subType, args)
			}
		})
	}
}

// TestBuildMPVArgsExtraArgsBracketedWhenSubtitleAndLavfFormat verifies that
// when ExtraArgs contain --demuxer-lavf-format= and a subtitle is loaded, the
// extra args are wrapped in per-file brackets so the format override does not
// bleed onto the external subtitle file open.
func TestBuildMPVArgsExtraArgsBracketedWhenSubtitleAndLavfFormat(t *testing.T) {
	args := buildMPVArgs(provider.MediaSource{
		URL:       "https://example.com/stream.m3u8",
		ExtraArgs: []string{"--demuxer=lavf", "--demuxer-lavf-format=hls"},
	}, model.ResolvedMedia{
		SelectedSubtitle: testSelectedSubtitle("/tmp/subtitles.srt"),
	}, "/tmp/sock", nil)

	if !containsArg(args, "--sub-file=/tmp/subtitles.srt") {
		t.Fatalf("external subtitle argument missing: %v", args)
	}
	wantTail := []string{"--{", "--demuxer=lavf", "--demuxer-lavf-format=hls", "--}", "--input-ipc-server=/tmp/sock", "https://example.com/stream.m3u8"}
	if len(args) < len(wantTail) {
		t.Fatalf("args too short (%d), want tail %v", len(args), wantTail)
	}
	if gotTail := args[len(args)-len(wantTail):]; !reflect.DeepEqual(gotTail, wantTail) {
		t.Fatalf("per-file bracket ordering wrong:\n got  %v\n want %v", gotTail, wantTail)
	}
	subIdx, bracketIdx := indexOf(args, "--sub-file=/tmp/subtitles.srt"), indexOf(args, "--{")
	if subIdx < 0 || bracketIdx < 0 || subIdx >= bracketIdx {
		t.Fatalf("--sub-file must precede --{ bracket: sub=%d bracket=%d", subIdx, bracketIdx)
	}
}

// TestBuildMPVArgsExtraArgsNoBracketWithoutLavfFormat confirms ExtraArgs
// without --demuxer-lavf-format= pass through without brackets.
func TestBuildMPVArgsExtraArgsNoBracketWithoutLavfFormat(t *testing.T) {
	args := buildMPVArgs(provider.MediaSource{
		URL:       "https://example.com/stream.m3u8",
		ExtraArgs: []string{"--demuxer=lavf"},
	}, model.ResolvedMedia{
		SelectedSubtitle: testSelectedSubtitle("/tmp/subtitles.srt"),
	}, "/tmp/sock", nil)

	wantTail := []string{"--demuxer=lavf", "--input-ipc-server=/tmp/sock", "https://example.com/stream.m3u8"}
	if len(args) < len(wantTail) {
		t.Fatalf("args too short: %v", args)
	}
	if gotTail := args[len(args)-len(wantTail):]; !reflect.DeepEqual(gotTail, wantTail) {
		t.Fatalf("got tail %v, want %v", gotTail, wantTail)
	}
	if containsArg(args, "--{") {
		t.Fatalf("no bracket expected when no lavf-format override: %v", args)
	}
}

// TestIINAExtraArgsStripsLavfFormat verifies iinaExtraArgs removes
// --demuxer-lavf-format= when a subtitle path is present.
func TestIINAExtraArgsStripsLavfFormat(t *testing.T) {
	extra := []string{"--demuxer=lavf", "--demuxer-lavf-format=hls", "--demuxer-lavf-o=extension_picky=0"}
	got := iinaExtraArgs(extra, "/tmp/sub.srt")
	for _, a := range got {
		if strings.HasPrefix(a, "--demuxer-lavf-format=") {
			t.Fatalf("must strip --demuxer-lavf-format= when subtitle present, got %v", got)
		}
	}
	if !containsArg(got, "--demuxer=lavf") || !containsArg(got, "--demuxer-lavf-o=extension_picky=0") {
		t.Fatalf("other ExtraArgs must be preserved: %v", got)
	}
}

// TestIINAExtraArgsNostripWithoutSubtitle confirms iinaExtraArgs is a no-op
// when no subtitle is present.
func TestIINAExtraArgsNostripWithoutSubtitle(t *testing.T) {
	extra := []string{"--demuxer=lavf", "--demuxer-lavf-format=hls"}
	if got := iinaExtraArgs(extra, ""); !reflect.DeepEqual(got, extra) {
		t.Fatalf("iinaExtraArgs without subtitle must not modify args: got %v", got)
	}
}

func TestBuildMPVArgsRejectsUnselectedRemoteSubtitles(t *testing.T) {
	media := model.ResolvedMedia{
		Subtitles:        []model.SubtitleTrack{{URL: "https://cdn.example.com/en.vtt"}},
		SelectedSubtitle: testSelectedSubtitle("/tmp/en.srt"),
	}
	args := buildMPVArgs(
		provider.MediaSource{URL: "https://example.com/stream.m3u8", Referer: "https://www.movy.sx/"},
		media,
		"/tmp/sock",
		nil,
	)
	if !containsArg(args, "--sub-file=/tmp/en.srt") {
		t.Fatalf("selected subtitle argument missing: %v", args)
	}
	if containsArg(args, "--sub-file=https://cdn.example.com/en.vtt") {
		t.Fatalf("unselected remote subtitle reached mpv: %v", args)
	}
	if !containsArg(args, "--referrer=https://www.movy.sx/") {
		t.Fatalf("source referrer missing: %v", args)
	}
}

func TestBuildMPVArgsIncludesExtraArgs(t *testing.T) {
	source := provider.MediaSource{
		URL:       "https://example.com/stream.m3u8",
		ExtraArgs: []string{"--demuxer-lavf-o=timeout=5000000"},
	}
	media := model.ResolvedMedia{
		SeriesTitle: "Inception",
	}

	args := buildMPVArgs(source, media, "/tmp/sock", nil)

	foundExtra := false
	for _, a := range args {
		if a == "--demuxer-lavf-o=timeout=5000000" {
			foundExtra = true
			break
		}
	}
	if !foundExtra {
		t.Errorf("buildMPVArgs missing ExtraArgs: %v", args)
	}
}
func TestOriginFromReferer(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://megaplay.buzz/", "https://megaplay.buzz"},
		{"https://megaplay.buzz", "https://megaplay.buzz"},
		{"https://example.com/embed/abc123", "https://example.com"},
		{"https://example.com/path?q=1#frag", "https://example.com"},
		{"http://cdn.example.org/stream.m3u8", "http://cdn.example.org"},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		got := originFromReferer(tt.in)
		if got != tt.want {
			t.Errorf("originFromReferer(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildMPVArgsOriginIsSchemeHost(t *testing.T) {
	source := provider.MediaSource{
		URL:     "https://cdn.example.com/stream.m3u8",
		Referer: "https://megaplay.buzz/watch/12345",
	}
	args := buildMPVArgs(source, model.ResolvedMedia{}, "/tmp/sock", nil)
	for _, a := range args {
		if strings.HasPrefix(a, "--http-header-fields=") {
			if strings.Contains(a, "megaplay.buzz/watch") {
				t.Errorf("Origin must not contain path; got header arg: %q", a)
			}
			if !strings.Contains(a, "Origin: https://megaplay.buzz") {
				t.Errorf("Origin missing scheme://host; got header arg: %q", a)
			}
			return
		}
	}
	t.Errorf("--http-header-fields arg not found in: %v", args)
}

func TestBuildCurlArgsRejectsHTTPErrorBody(t *testing.T) {
	args := buildCurlArgs("https://cdn.example.com/video.m3u8", nil)
	if !containsArg(args, "--fail") {
		t.Fatalf("curl args must reject HTTP error bodies: %v", args)
	}
}

func TestLaunchResultRejectsFailedCurlProducer(t *testing.T) {
	result := launchResult{
		launched:   true,
		quickExit:  true,
		curlFailed: true,
		playback:   PlaybackResult{FinalPositionSecs: 1},
	}
	if result.succeeded() {
		t.Fatal("curl failure must not be reported as successful playback")
	}
}

func TestRegistryPreservesCompletionConfirmation(t *testing.T) {
	fallbackCalled := false
	registry := &Registry{preferred: "confirm"}
	registry.players = []Player{
		&stubPlayer{
			name:      "confirm",
			available: true,
			play: func() (PlaybackResult, error) {
				return PlaybackResult{}, &NeedsCompletionConfirmError{}
			},
		},
		&stubPlayer{
			name:      "mpv",
			available: true,
			play: func() (PlaybackResult, error) {
				fallbackCalled = true
				return PlaybackResult{}, nil
			},
		},
	}

	_, err := registry.PlayWithSources(
		[]provider.MediaSource{{URL: "https://cdn.example.com/video.m3u8"}},
		model.ResolvedMedia{},
		"confirm",
	)
	var confirm *NeedsCompletionConfirmError
	if !errors.As(err, &confirm) {
		t.Fatalf("error = %v, want NeedsCompletionConfirmError", err)
	}
	if fallbackCalled {
		t.Fatal("successful external launch must not fall through to another player")
	}
}

func TestRegistryDoesNotFallBackToAnotherPlayerOnError(t *testing.T) {
	fallbackCalled := false
	expectedErr := errors.New("mpv crashed")
	registry := &Registry{preferred: "mpv"}
	registry.players = []Player{
		&stubPlayer{
			name:      "mpv",
			available: true,
			play: func() (PlaybackResult, error) {
				return PlaybackResult{}, expectedErr
			},
		},
		&stubPlayer{
			name:      "iina",
			available: true,
			play: func() (PlaybackResult, error) {
				fallbackCalled = true
				return PlaybackResult{}, nil
			},
		},
	}

	_, err := registry.PlayWithSources(
		[]provider.MediaSource{{URL: "https://cdn.example.com/video.m3u8"}},
		model.ResolvedMedia{},
		"mpv",
	)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
	if fallbackCalled {
		t.Fatal("player error must not fall back to another player; only the selected player should be tried")
	}
}

func TestMPVLoadsSelectedLocalSubtitle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lavfi test source is not portable to Windows CI")
	}
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv not installed")
	}

	subtitlePath := filepath.Join(t.TempDir(), "subtitle.srt")
	subtitle := "1\n00:00:00,000 --> 00:00:01,000\nkari mpv subtitle test\n"
	if err := os.WriteFile(subtitlePath, []byte(subtitle), 0o600); err != nil {
		t.Fatal(err)
	}
	media := model.ResolvedMedia{SelectedSubtitle: testSelectedSubtitle(subtitlePath)}
	source := provider.MediaSource{
		URL: "av://lavfi:testsrc=duration=1:size=160x90:rate=10",
		ExtraArgs: []string{
			"--vo=null",
			"--ao=null",
			"--length=0.5",
		},
	}

	if _, err := playSingleSource(source, media, nil, nil); err != nil {
		t.Fatalf("playSingleSource: %v", err)
	}
}

func TestIsShortWarningVideo(t *testing.T) {
	tests := []struct {
		name   string
		dur    float64
		url    string
		isLive bool
		want   bool
	}{
		{"45s cloudflare warning video", 45.0, "https://cdn.example.com/stream.m3u8", false, true},
		{"exactly 60s warning clip", 60.0, "https://cdn.example.com/stream.m3u8", false, true},
		{"61s stream", 61.0, "https://cdn.example.com/stream.m3u8", false, false},
		{"real 24-minute episode", 1440.0, "https://cdn.example.com/stream.m3u8", false, false},
		{"real movie (2 hours)", 7200.0, "https://cdn.example.com/stream.m3u8", false, false},
		{"live stream with short buffer", 45.0, "https://cdn.example.com/live.m3u8", true, false},
		{"synthetic test source", 1.0, "av://lavfi:testsrc", false, false},
		{"empty or zero duration", 0.0, "https://cdn.example.com/stream.m3u8", false, false},
		{"negative duration", -1.0, "https://cdn.example.com/stream.m3u8", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isShortWarningVideo(tc.dur, tc.url, tc.isLive)
			if got != tc.want {
				t.Errorf("isShortWarningVideo(%.1f, %q, %v) = %v, want %v", tc.dur, tc.url, tc.isLive, got, tc.want)
			}
		})
	}
}

func TestAttemptSourcesSkipsShortWarningVideo(t *testing.T) {
	sources := []provider.MediaSource{
		{URL: "https://bad.example.com/45s.m3u8", Quality: "1080p Bad"},
		{URL: "https://good.example.com/real.m3u8", Quality: "1080p Good"},
	}

	tried := []string{}
	res, err := attemptSources("mpv", sources, func(src provider.MediaSource) (PlaybackResult, error) {
		tried = append(tried, src.URL)
		if src.URL == "https://bad.example.com/45s.m3u8" {
			return PlaybackResult{}, fmt.Errorf("source rejected: stream is a short placeholder/warning video (45.0s <= 60s)")
		}
		return PlaybackResult{DurationSecs: 1440, FinalPositionSecs: 1400}, nil
	})

	if err != nil {
		t.Fatalf("attemptSources failed: %v", err)
	}
	if len(tried) != 2 {
		t.Fatalf("expected 2 tried sources, got %d", len(tried))
	}
	if res.DurationSecs != 1440 {
		t.Errorf("expected good source result (1440s), got %.1f", res.DurationSecs)
	}
}
