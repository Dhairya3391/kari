package q62

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"kari/internal/config"
	"kari/internal/lang"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// episodeHandleSep separates the season and episode IDs Q62 addresses an
// episode by. Both IDs are alphanumeric, so the separator is unambiguous.
const episodeHandleSep = "|"

// maxReleases caps how many signed descriptors one episode probes. Q62 rarely
// offers more than three, and the cap keeps a large release list from spending
// the provider's whole resolution budget on HLS manifest fetches.
const maxReleases = 3

// releaseSummary is one quality/rip variant listed before resolution.
type releaseSummary struct {
	ID           string   `json:"id"`
	Resolution   string   `json:"resolution"`
	RipType      string   `json:"ripType"`
	AudioLocales []string `json:"audioLocales"`
}

// releaseDetail is the signed descriptor for one release. Its media URLs are
// relative to Q62's proxy and carry the access token as a query parameter.
type releaseDetail struct {
	ID               string            `json:"id"`
	M3U8URL          string            `json:"m3u8Url"`
	OriginalLanguage string            `json:"originalLanguage"`
	Video            []releaseVideo    `json:"video"`
	Audios           []releaseAudio    `json:"audios"`
	Subtitles        []releaseSubtitle `json:"subtitles"`
}

type releaseVideo struct {
	Resolution   int    `json:"resolution"`
	RelativePath string `json:"relativePath"`
}

type releaseAudio struct {
	Language string `json:"language"`
	Channels int    `json:"channels"`
}

type releaseSubtitle struct {
	Language     string `json:"language"`
	Type         string `json:"type"`
	RelativePath string `json:"relativePath"`
}

// coordinates is the fully resolved Q62 address of one playable item.
type coordinates struct {
	contentID string
	seasonID  string
	episodeID string
	tv        bool
}

// ResolveSource implements provider.EpisodeSource by draining the incremental
// fan-out into a single source list.
func (c *Client) ResolveSource(ctx context.Context, mediaID string, episode provider.Episode) ([]provider.MediaSource, error) {
	coords, err := c.resolveCoordinates(ctx, mediaID, episode)
	if err != nil {
		return nil, err
	}
	updates := make(chan []provider.MediaSource, maxReleases)
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.fetchSources(ctx, coords, updates)
		close(updates)
	}()

	var all []provider.MediaSource
	for batch := range updates {
		all = append(all, batch...)
	}
	err = <-errCh
	if len(all) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, provider.ErrNoSources
	}
	return all, nil
}

// ResolveStream implements provider.StreamingProvider, pushing each release's
// playable rows as soon as its signed descriptor has been expanded.
func (c *Client) ResolveStream(ctx context.Context, mediaID string, episode provider.Episode, updates chan<- []provider.MediaSource) error {
	coords, err := c.resolveCoordinates(ctx, mediaID, episode)
	if err != nil {
		return err
	}
	return c.fetchSources(ctx, coords, updates)
}

// resolveCoordinates turns the generic media handle into Q62's own IDs. A
// numeric media ID is a TMDB ID from another provider and is mapped through
// the catalog index; TV episodes additionally need episode IDs, which are
// recovered from the handle or by number when the handle is foreign.
func (c *Client) resolveCoordinates(ctx context.Context, mediaID string, episode provider.Episode) (coordinates, error) {
	tv := episode.Season > 0 || episode.Episode > 0
	contentID, err := c.contentID(ctx, mediaID, episode.TMDBID, tv)
	if err != nil {
		return coordinates{}, err
	}
	coords := coordinates{contentID: contentID, tv: tv}
	if !tv {
		return coords, nil
	}

	parts := strings.Split(strings.TrimSpace(episode.ID), episodeHandleSep)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		coords.seasonID, coords.episodeID = parts[0], parts[1]
		return coords, nil
	}
	return c.coordinatesByNumber(ctx, coords, episode)
}

// coordinatesByNumber recovers season and episode IDs from numbered handles,
// which is what episodes contributed by another provider look like.
func (c *Client) coordinatesByNumber(ctx context.Context, coords coordinates, episode provider.Episode) (coordinates, error) {
	seasons, err := getJSON[[]seasonInfo](ctx, c, "/tvshow/"+url.PathEscape(coords.contentID)+"/season")
	if err != nil {
		return coordinates{}, fmt.Errorf("q62 resolve seasons: %w", err)
	}
	for _, season := range seasons {
		if season.SeasonNumber != episode.Season || season.ID == "" {
			continue
		}
		items, err := getJSON[[]episodeInfo](ctx, c, "/season/"+url.PathEscape(season.ID)+"/episode")
		if err != nil {
			return coordinates{}, fmt.Errorf("q62 resolve episodes: %w", err)
		}
		for _, item := range items {
			if item.EpisodeNumber == episode.Episode && item.ID != "" {
				coords.seasonID, coords.episodeID = season.ID, item.ID
				return coords, nil
			}
		}
	}
	return coordinates{}, provider.ErrNoSources
}

// fetchReleaseSummaries lists the releases for a movie or episode and returns
// the path prefix used to fetch each signed descriptor.
func (c *Client) fetchReleaseSummaries(ctx context.Context, coords coordinates) ([]releaseSummary, string, error) {
	base := "/movie/" + url.PathEscape(coords.contentID)
	if coords.tv {
		base = "/season/" + url.PathEscape(coords.seasonID) + "/episode/" + url.PathEscape(coords.episodeID)
	}
	summaries, err := getJSON[[]releaseSummary](ctx, c, base+"/releases")
	if err != nil {
		if isNotFound(err) {
			return nil, "", provider.ErrNoSources
		}
		return nil, "", fmt.Errorf("q62 releases: %w", err)
	}
	return summaries, base + "/release", nil
}

// fetchSources resolves every shortlisted release concurrently, publishing one
// batch per release. A failed release only removes its own rows.
func (c *Client) fetchSources(ctx context.Context, coords coordinates, updates chan<- []provider.MediaSource) error {
	summaries, releasePath, err := c.fetchReleaseSummaries(ctx, coords)
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		return provider.ErrNoSources
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		return releaseRank(summaries[i]) > releaseRank(summaries[j])
	})
	if len(summaries) > maxReleases {
		summaries = summaries[:maxReleases]
	}

	var (
		mu   sync.Mutex
		sent int
	)
	g, gCtx := errgroup.WithContext(ctx)
	for _, summary := range summaries {
		g.Go(func() error {
			target := releasePath + "?releaseId=" + url.QueryEscape(summary.ID) + "&locale=en"
			detail, err := getJSON[releaseDetail](gCtx, c, target)
			if err != nil {
				logPartialFailure(err)
				return nil
			}
			sources := c.sourcesFromRelease(gCtx, detail, summary)
			if len(sources) == 0 {
				return nil
			}
			select {
			case updates <- sources:
				mu.Lock()
				sent += len(sources)
				mu.Unlock()
				return nil
			case <-gCtx.Done():
				return gCtx.Err()
			}
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if sent == 0 {
		return provider.ErrNoSources
	}
	return nil
}

// sourcesFromRelease expands one signed HLS master into quality tiers plus an
// adaptive master row. Truly video-only variants are dropped: their bare URL
// would play without sound.
func (c *Client) sourcesFromRelease(ctx context.Context, detail releaseDetail, summary releaseSummary) []provider.MediaSource {
	master := c.mediaURL(detail.M3U8URL, "")
	if master == "" {
		return nil
	}
	variants, err := kit.ParseHLSVariants(ctx, c.httpClient, master, map[string]string{
		"Referer": c.siteBase + "/",
	})
	if err != nil {
		logPartialFailure(err)
	}

	backend := releaseBackend(summary)
	languages := audioLanguages(detail.Audios)
	subtitles := c.subtitleOptions(detail.M3U8URL, detail.Subtitles)

	var (
		sources     []provider.MediaSource
		bestHeight  int
		bestBitrate int
	)
	for _, variant := range variants {
		if variant.AudioGroup != "" && !hasAudioCodec(variant.Codecs) {
			continue
		}
		height := kit.CredibleHeight(variant.Height, variant.Codecs)
		if height <= 0 {
			continue
		}
		// A muxed variant already carries its audio; pointing it at the master
		// would attach the same track again as an external input.
		manifest := ""
		if !hasAudioCodec(variant.Codecs) {
			manifest = master
		}
		sources = append(sources, c.newSource(variant.URL, manifest, kit.QualityLabelFromHeight(height), backend, height, variant.Bandwidth, languages, subtitles))
		if height > bestHeight {
			bestHeight = height
		}
		if variant.Bandwidth > bestBitrate {
			bestBitrate = variant.Bandwidth
		}
	}
	sources = append(sources, c.newSource(master, master, "Auto", backend, bestHeight, bestBitrate, languages, subtitles))
	return provider.FilterDirectSources(sources)
}

// mediaURL resolves a descriptor's relative path against Q62's proxy, keeping
// the signed token query intact. An empty child yields the descriptor's own
// media file; a relative child is resolved next to it.
func (c *Client) mediaURL(descriptor, child string) string {
	parsed, err := url.Parse(strings.TrimSpace(descriptor))
	if err != nil || parsed.Path == "" {
		return ""
	}
	target := parsed.Path
	if child != "" {
		target = path.Join(path.Dir(parsed.Path), child)
	}
	out := c.proxyBase + target
	if parsed.RawQuery != "" {
		out += "?" + parsed.RawQuery
	}
	return out
}

// subtitleOptions turns subtitle renditions into playable tracks served by
// the same signed proxy as the stream.
func (c *Client) subtitleOptions(descriptor string, subtitles []releaseSubtitle) []provider.SubtitleOption {
	out := make([]provider.SubtitleOption, 0, len(subtitles))
	seen := make(map[string]bool)
	for _, sub := range subtitles {
		target := c.mediaURL(descriptor, sub.RelativePath)
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		code := languageCode(sub.Language)
		if code == "" {
			code = "und"
		}
		out = append(out, provider.SubtitleOption{URL: target, Language: code})
	}
	return out
}

// audioLanguages returns the deduplicated normalized language codes declared
// by a release's audio renditions.
func audioLanguages(audios []releaseAudio) []string {
	seen := make(map[string]bool, len(audios))
	var out []string
	for _, audio := range audios {
		code := languageCode(audio.Language)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

// hasAudioCodec reports whether a variant's CODECS list includes an audio
// codec. Q62 declares an AUDIO group on muxed variants too, so the group alone
// does not make a rendition video-only; the codec list decides.
func hasAudioCodec(codecs string) bool {
	for _, codec := range strings.Split(strings.ToLower(codecs), ",") {
		codec = strings.TrimSpace(codec)
		switch {
		case strings.HasPrefix(codec, "mp4a"),
			strings.HasPrefix(codec, "ac-3"),
			strings.HasPrefix(codec, "ec-3"),
			strings.HasPrefix(codec, "alac"),
			strings.HasPrefix(codec, "opus"),
			strings.HasPrefix(codec, "vorbis"),
			strings.HasPrefix(codec, "flac"):
			return true
		}
	}
	return false
}

// languageCode strips a region subtag before normalization so Q62's locale
// tags ("en-US", "pt-BR") collapse to the canonical language code Kari uses
// for audio and subtitle filtering.
func languageCode(raw string) string {
	value := strings.TrimSpace(raw)
	if i := strings.IndexAny(value, "-_"); i == 2 || i == 3 {
		return lang.Normalize(value[:i])
	}
	return lang.Normalize(value)
}

// newSource builds one playable HLS row. Q62's proxy serves the manifests
// without origin checks, so only the browser user agent is attached.
func (c *Client) newSource(target, manifestURL, quality, backend string, height, bitrate int, languages []string, subtitles []provider.SubtitleOption) provider.MediaSource {
	return provider.MediaSource{
		URL:         target,
		ManifestURL: manifestURL,
		Quality:     quality,
		Backend:     backend,
		Type:        provider.SourceTypeHLS,
		Referer:     c.siteBase + "/",
		UserAgent:   config.DesktopUserAgent,
		Height:      height,
		Bitrate:     bitrate,
		Subtitles:   subtitles,
		Audio:       provider.AudioInfo{Languages: languages},
	}
}

// releaseBackend describes the upstream encode independently of quality.
func releaseBackend(summary releaseSummary) string {
	if summary.RipType == "" {
		return summary.Resolution
	}
	return summary.Resolution + "/" + summary.RipType
}

// releaseRank orders releases so the highest resolution and the least
// camera-sourced rip are shortlisted first.
func releaseRank(summary releaseSummary) int {
	rank := 0
	switch strings.ToUpper(summary.Resolution) {
	case "UHD_8K":
		rank = 80
	case "UHD_4K":
		rank = 70
	case "FHD_1080P":
		rank = 60
	case "HD_720P":
		rank = 50
	case "SD":
		rank = 40
	case "CAM":
		rank = 10
	default:
		rank = 30
	}
	switch strings.ToUpper(summary.RipType) {
	case "WEB":
		rank++
	case "BR":
		rank += 2
	}
	return rank
}
