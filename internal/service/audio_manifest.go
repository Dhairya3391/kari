package service

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// manifestProbeBytes caps the playlist body read. Masters are small, and the
// bound keeps a broken CDN from turning a metadata probe into a large download.
const manifestProbeBytes = 256 << 10

// probeRequest builds a playlist GET with the playback headers a CDN expects.
// A source with no explicit User-Agent still needs a browser identity: Go's
// default agent is a common reason a master answers 403 and its quality ladder
// is never read.
func probeRequest(ctx context.Context, src provider.MediaSource) (*http.Request, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, false
	}
	if src.Referer != "" {
		req.Header.Set("Referer", src.Referer)
		if !src.SuppressOrigin {
			if u, parseErr := url.Parse(src.Referer); parseErr == nil && u.Scheme != "" && u.Host != "" {
				req.Header.Set("Origin", u.Scheme+"://"+u.Host)
			}
		}
	}
	userAgent := src.UserAgent
	if strings.TrimSpace(userAgent) == "" {
		userAgent = config.DesktopUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	if src.CookieHeader != "" {
		req.Header.Set("Cookie", src.CookieHeader)
	}
	return req, true
}

// fetchManifestText performs one bounded playlist GET and returns its body.
func fetchManifestText(ctx context.Context, client *http.Client, src provider.MediaSource) (string, bool) {
	req, ok := probeRequest(ctx, src)
	if !ok {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, manifestProbeBytes))
	_ = resp.Body.Close()
	if readErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	return string(body), true
}

// discoverManifestAudio adds track languages advertised by an HLS master. It
// runs after sources are already visible, so slow CDNs never delay the UI. The
// discovered codes become the source's audio identity so the displayed label,
// the language filter, and mpv's --alang all agree on one language.
func discoverManifestAudio(ctx context.Context, client *http.Client, sources []provider.MediaSource) []provider.MediaSource {
	probeCtx, cancel := context.WithTimeout(ctx, manifestBatchProbeTimeout)
	defer cancel()

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, manifestProbeConcurrency)
	for i := range sources {
		if sources[i].Type != provider.SourceTypeHLS && !isHLSURL(sources[i].URL) && !isHLSURL(sources[i].ManifestURL) {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-probeCtx.Done():
				return
			}
			probeSrc := sources[index]
			if probeSrc.ManifestURL != "" {
				probeSrc.URL = probeSrc.ManifestURL
			}
			text, ok := fetchManifestText(probeCtx, client, probeSrc)
			if !ok {
				return
			}
			tracks := hlsAudioTracks(text)
			if len(tracks) == 0 {
				return
			}
			codes := make([]string, 0, len(tracks))
			for _, track := range tracks {
				codes = append(codes, track.Code)
			}
			src := &sources[index]
			src.Audio.Languages = codes
			src.AudioLabel = lang.Names(codes)
			src.PreferredAudio = codes[0]
		}(i)
	}
	wg.Wait()
	return sources
}

// sourceEnrichment is one finished master probe, applied by the collector
// goroutine so the source slice is only ever written from one place.
type sourceEnrichment struct {
	index   int
	bitrate int
	quality string
}

// enrichAdaptiveSources probes only adaptive ("Auto"/"Master") HLS rows,
// attaching the declared peak bitrate and the real maximum quality label. It
// publishes a snapshot as each probe finishes so the UI shows rows as they
// complete instead of waiting for the whole batch. The returned bool reports
// whether any snapshot was published, so callers can avoid a redundant raw
// publish when there was nothing to enrich.
func enrichAdaptiveSources(ctx context.Context, client *http.Client, sources []provider.MediaSource, mode provider.ContentType, onUpdate func([]provider.MediaSource)) ([]provider.MediaSource, bool) {
	if len(sources) == 0 || mode == provider.ModeLive {
		return sources, false
	}
	indices := make([]int, 0, len(sources))
	for i := range sources {
		src := &sources[i]
		if (src.Type != provider.SourceTypeHLS && !isHLSURL(src.URL) && !isHLSURL(src.ManifestURL)) || !kit.IsAdaptiveQuality(src.Quality) {
			continue
		}
		indices = append(indices, i)
	}
	if len(indices) == 0 {
		return sources, false
	}

	probeCtx, cancel := context.WithTimeout(ctx, manifestQualityProbeTimeout)
	defer cancel()
	results := make(chan sourceEnrichment, len(indices))
	semaphore := make(chan struct{}, manifestProbeConcurrency)
	var wg sync.WaitGroup
	for _, index := range indices {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-probeCtx.Done():
				return
			}
			probeSrc := sources[i]
			manifestURL := probeSrc.URL
			if probeSrc.ManifestURL != "" {
				probeSrc.URL = probeSrc.ManifestURL
				manifestURL = probeSrc.ManifestURL
			}
			text, ok := fetchManifestText(probeCtx, client, probeSrc)
			if !ok {
				return
			}
			src := sources[i]
			relabeled := false
			enrichSourceFromVariants(&src, kit.ParseHLSVariantsText(manifestURL, text), &relabeled)
			if src.Bitrate == 0 && !relabeled {
				return
			}
			results <- sourceEnrichment{index: i, bitrate: src.Bitrate, quality: src.Quality}
		}(index)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	relabeled := make([]bool, len(sources))
	changed := false
	apply := func(got sourceEnrichment) {
		if got.bitrate > sources[got.index].Bitrate {
			sources[got.index].Bitrate = got.bitrate
			changed = true
		}
		if got.quality != "" && got.quality != sources[got.index].Quality {
			sources[got.index].Quality = got.quality
			relabeled[got.index] = true
			changed = true
		}
	}
	remaining := len(indices)
collect:
	for remaining > 0 {
		select {
		case got, ok := <-results:
			if !ok {
				break collect
			}
			remaining--
			apply(got)
			if onUpdate != nil {
				onUpdate(cloneMediaSources(sources))
			}
		case <-probeCtx.Done():
			break collect
		}
	}
	for got := range results {
		apply(got)
	}
	final := dropShadowedTiers(sources, relabeled)
	if onUpdate != nil {
		onUpdate(cloneMediaSources(final))
	}
	return final, changed
}

// enrichSourceFromVariants attaches the bitrate and real peak-tier label of
// an HLS master to one source. A relabel only happens when the row is still
// adaptive, so providers that published an explicit tier keep their label.
func enrichSourceFromVariants(src *provider.MediaSource, variants []kit.HLSVariant, relabeled *bool) {
	maxBitrate, bestHeight := 0, 0
	for _, variant := range variants {
		if variant.Bandwidth > maxBitrate {
			maxBitrate = variant.Bandwidth
		}
		if height := kit.CredibleHeight(variant.Height, variant.Codecs); height > bestHeight {
			bestHeight = height
		}
	}
	if maxBitrate > src.Bitrate {
		src.Bitrate = maxBitrate
	}
	if bestHeight <= 0 || !kit.IsAdaptiveQuality(src.Quality) {
		return
	}
	label := kit.QualityLabelFromHeight(bestHeight)
	updated := kit.RelabelAdaptiveQuality(src.Quality, label)
	if updated != src.Quality {
		src.Quality = updated
		*relabeled = true
	}
}

// mergeManifestAudio carries every probe-discovered field onto the verified
// subset of a provider batch. Both slices come from the same batch; the probe
// runs on copies in parallel with verification, so fields are matched by URL.
func mergeManifestAudio(verified, enriched []provider.MediaSource) []provider.MediaSource {
	if len(enriched) == 0 {
		return verified
	}
	type probedFields struct {
		audio     string
		preferred string
		langs     []string
		quality   string
		bitrate   int
	}
	fields := make(map[string]probedFields, len(enriched))
	for _, src := range enriched {
		got := fields[src.URL]
		if got.audio == "" {
			got.audio = src.AudioLabel
		}
		if got.preferred == "" {
			got.preferred = src.PreferredAudio
		}
		if len(got.langs) == 0 {
			got.langs = src.Audio.Languages
		}
		if src.Bitrate > got.bitrate {
			got.bitrate = src.Bitrate
		}
		if got.quality == "" && !kit.IsAdaptiveQuality(src.Quality) {
			got.quality = src.Quality
		}
		fields[src.URL] = got
	}
	for i := range verified {
		got := fields[verified[i].URL]
		// Probed fields are the stream's own declared renditions, so they
		// outrank any provider-supplied guess. Providers that only know
		// the channel (its country or directory languages) leave them
		// empty, which keeps the table honest about single-track mirrors.
		if got.audio != "" {
			verified[i].AudioLabel = got.audio
		}
		if got.preferred != "" {
			verified[i].PreferredAudio = got.preferred
		}
		if len(got.langs) > 0 {
			verified[i].Audio.Languages = got.langs
		}
		if verified[i].Bitrate == 0 {
			verified[i].Bitrate = got.bitrate
		}
		if got.quality != "" && kit.IsAdaptiveQuality(verified[i].Quality) {
			verified[i].Quality = got.quality
		}
	}
	return verified
}

func isHLSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

type hlsAudioTrack struct {
	Code    string
	Default bool
}

func hlsAudioTracks(manifest string) []hlsAudioTrack {
	var tracks []hlsAudioTrack
	seen := make(map[string]int)
	scanner := bufio.NewScanner(strings.NewReader(manifest))
	scanner.Buffer(make([]byte, 1024), 64<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") || !strings.Contains(line, "TYPE=AUDIO") {
			continue
		}
		attrs := parseHLSAttributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
		code := canonicalAudioCode(attrs["LANGUAGE"], attrs["NAME"])
		if code == "" {
			continue
		}
		isDefault := strings.EqualFold(strings.TrimSpace(attrs["DEFAULT"]), "YES")
		if index, ok := seen[code]; ok {
			if isDefault {
				tracks[index].Default = true
			}
			continue
		}
		seen[code] = len(tracks)
		tracks = append(tracks, hlsAudioTrack{Code: code, Default: isDefault})
	}
	ordered := make([]hlsAudioTrack, 0, len(tracks))
	for _, track := range tracks {
		if track.Default {
			ordered = append(ordered, track)
		}
	}
	for _, track := range tracks {
		if !track.Default {
			ordered = append(ordered, track)
		}
	}
	return ordered
}

func canonicalAudioCode(language, name string) string {
	if code := lang.Normalize(language); code != "" && !strings.EqualFold(code, "und") {
		if base, _, regional := strings.Cut(code, "-"); regional && lang.Name(base) != strings.ToUpper(base) {
			return base
		}
		return code
	}
	label := strings.TrimSpace(name)
	if label == "" || strings.EqualFold(label, "audio") || strings.EqualFold(label, "unknown") {
		return ""
	}
	return lang.FromLabel(label)
}

func parseHLSAttributes(raw string) map[string]string {
	attrs := make(map[string]string)
	var key, value strings.Builder
	quoted, inValue := false, false
	flush := func() {
		if key.Len() > 0 {
			attrs[strings.TrimSpace(key.String())] = strings.Trim(strings.TrimSpace(value.String()), "\"")
		}
		key.Reset()
		value.Reset()
		inValue = false
	}
	for _, char := range raw {
		switch {
		case char == '"':
			quoted = !quoted
		case char == '=' && !quoted && !inValue:
			inValue = true
		case char == ',' && !quoted:
			flush()
		default:
			if inValue {
				value.WriteRune(char)
			} else {
				key.WriteRune(char)
			}
		}
	}
	flush()
	return attrs
}
