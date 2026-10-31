package anivault

import (
	"net/url"
	"path"
	"strings"

	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// This file maps AniVault watch payloads onto provider.MediaSource values,
// unwrapping the API's proxy URLs so mpv always receives direct CDN
// addresses and the upstream referer those CDNs require.

// episodeSource returns the playback source named by an episode handle.
// Handles from other anime providers have no AniVault source segment; those
// default to AniVault's own catalog source.
func episodeSource(episodeID string) string {
	parts := strings.Split(strings.TrimSpace(episodeID), "/")
	if len(parts) >= 5 && parts[0] == "watch" &&
		strings.EqualFold(strings.TrimSpace(parts[1]), sourceAnimeHeaven) {
		return sourceAnimeHeaven
	}
	return sourceAniVault
}

// candidateSources returns the sources to try for one resolution, in order.
// Subtitles fall back from AniVault to AnimeHeaven; dubs never use
// AnimeHeaven, which has no dub catalog.
func candidateSources(source, category string) []string {
	if source == sourceAnimeHeaven {
		return []string{sourceAnimeHeaven, sourceAniVault}
	}
	if category == provider.AudioSub {
		return []string{sourceAniVault, sourceAnimeHeaven}
	}
	return []string{sourceAniVault}
}

// watchSources maps a watch payload to direct playable sources. Iframe-only
// payloads carry no stream and are dropped.
func watchSources(watch watchResponse) []provider.MediaSource {
	if watch.IframeOnly {
		return nil
	}
	subtitles := subtitleOptions(watch.Subtitles)
	subType := watchSubType(watch, subtitles)
	quality := "Auto"
	embedReferer := refererFromEmbed(watch.EmbedURL)
	seen := make(map[string]bool, 3)
	var out []provider.MediaSource
	appendSource := func(rawURL, proxyURL, streamType string) {
		direct, referer := directStream(rawURL, proxyURL)
		if direct == "" || seen[direct] {
			return
		}
		seen[direct] = true
		var extraArgs []string
		if streamType == provider.SourceTypeHLS {
			extraArgs = hlsExtraArgs(direct)
		}
		out = append(out, provider.MediaSource{
			URL:       direct,
			Quality:   quality,
			Backend:   firstNonEmpty(watch.Server, watch.Source),
			Referer:   firstNonEmpty(referer, embedReferer),
			Type:      streamType,
			Subtitles: subtitles,
			SubType:   subType,
			ExtraArgs: extraArgs,
		})
	}
	appendSource(watch.M3U8, watch.HLSProxyURL, provider.SourceTypeHLS)
	appendSource(watch.MP4, watch.MP4ProxyURL, provider.SourceTypeMP4)
	rawDirect, _ := directStream(watch.RawStreamURL, watch.StreamURL)
	rawType := provider.SourceTypeMP4
	if rawDirect != "" {
		rawType = kit.NormalizeSourceKind(rawDirect)
	}
	appendSource(watch.RawStreamURL, watch.StreamURL, rawType)
	return out
}

// watchSubType labels the subtitle kind carried by a stream. AnimeHeaven's
// MP4s have burned-in subtitles and no sidecar tracks; Anikoto-backed HLS
// streams are clean video with separate VTT tracks. Streams with neither a
// known hard-sub origin nor sidecar captions stay unlabelled.
func watchSubType(watch watchResponse, subtitles []provider.SubtitleOption) string {
	if strings.EqualFold(strings.TrimSpace(watch.Source), sourceAnimeHeaven) ||
		strings.Contains(strings.ToLower(watch.Server), "animeheaven") {
		return provider.SubTypeHard
	}
	if len(subtitles) > 0 {
		return provider.SubTypeSoft
	}
	return ""
}

// directStream prefers the API's direct URL and borrows the upstream referer
// from its matching proxy URL. When only a proxy is present, the proxy is
// unwrapped to the direct address it forwards to.
func directStream(direct, proxy string) (string, string) {
	direct = strings.TrimSpace(direct)
	proxy = strings.TrimSpace(proxy)
	if direct != "" {
		return direct, proxyReferer(proxy)
	}
	return unwrapProxy(proxy)
}

// unwrapProxy turns an AniVault /proxy/ URL into the direct target it
// forwards to, returning the upstream referer alongside. Non-proxy URLs pass
// through unchanged.
func unwrapProxy(rawURL string) (string, string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, ""
	}
	if !strings.Contains(strings.ToLower(parsed.Path), "/proxy/") {
		return rawURL, ""
	}
	target := strings.TrimSpace(parsed.Query().Get("url"))
	if target == "" {
		return rawURL, proxyReferer(rawURL)
	}
	return target, strings.TrimSpace(parsed.Query().Get("ref"))
}

// proxyReferer extracts the ref query parameter an AniVault proxy URL carries.
func proxyReferer(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("ref"))
}

// refererFromEmbed derives the upstream site referer from the embed URL.
func refererFromEmbed(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host + "/"
}

// subtitleOptions unwraps proxied subtitle tracks and drops thumbnail
// previews, which are sprite metadata rather than caption text.
func subtitleOptions(entries []apiSubtitle) []provider.SubtitleOption {
	out := make([]provider.SubtitleOption, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		target, referer := unwrapProxy(entry.URL)
		if target == "" || isThumbnailTrack(target) || seen[target] {
			continue
		}
		language := lang.Normalize(kit.Clean(entry.Lang))
		if language == "unknown" {
			continue
		}
		if language == "" {
			language = "en"
		}
		seen[target] = true
		out = append(out, provider.SubtitleOption{
			URL:      target,
			Language: language,
			Default:  entry.Default,
			Referer:  referer,
		})
	}
	return out
}

// isThumbnailTrack reports whether a subtitle URL is a preview-thumbnail
// sprite track.
func isThumbnailTrack(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.Contains(lower, "thumbnails") || strings.Contains(lower, "thumbnail.")
}

// hlsExtraArgs forces the HLS demuxer for playlists without a .m3u8 suffix,
// which mpv otherwise cannot sniff reliably.
func hlsExtraArgs(rawURL string) []string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil
	}
	if strings.HasSuffix(strings.ToLower(parsed.Path), ".m3u8") {
		return nil
	}
	if strings.Contains(strings.ToLower(path.Base(parsed.Path)), ".m3u8") {
		return nil
	}
	return []string{"--demuxer=lavf", "--demuxer-lavf-format=hls"}
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
