package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/subtitles"
	"kari/internal/util"
)

const (
	subtitleCacheSize               = 100
	subtitleFetchTimeout            = 10 * time.Second
	subtitleProviderCandidateLimit  = 3
	subtitleProviderDownloadTimeout = 3 * time.Second
)

// SubtitleService selects and materializes one subtitle track for playback.
type SubtitleService struct {
	openSubtitles openSubtitleSource
	httpClient    *http.Client
	cache         *util.BoundedCache[model.SubtitleTrack]
	cacheVersion  atomic.Uint64
	disableMu     sync.RWMutex
	disableAnime  bool
}

type openSubtitleSource interface {
	Configured() bool
	FetchBestSubtitle(context.Context, string, string, int, int, int) (model.SubtitleTrack, bool, error)
}

// SetDisableAnimeSubtitles toggles anime-specific subtitle suppression.
func (s *SubtitleService) SetDisableAnimeSubtitles(disable bool) {
	s.disableMu.Lock()
	s.disableAnime = disable
	s.disableMu.Unlock()
}

// ClearCache removes cached subtitle selections so the next fetch resolves
// tracks again from the current provider results.
func (s *SubtitleService) ClearCache() {
	s.cacheVersion.Add(1)
	s.cache.Clear()
}

// NewSubtitleService builds the service; OpenSubtitles stays unconfigured
// unless credentials are present.
func NewSubtitleService(cfg *config.Config) *SubtitleService {
	if cfg == nil {
		cfg = &config.Config{}
	}
	var openSubtitles openSubtitleSource
	if strings.TrimSpace(cfg.OpenSubtitlesKey) != "" &&
		strings.TrimSpace(cfg.OpenSubtitlesUser) != "" &&
		strings.TrimSpace(cfg.OpenSubtitlesPass) != "" {
		openSubtitles = subtitles.NewClient(
			cfg.OpenSubtitlesKey,
			cfg.OpenSubtitlesUser,
			cfg.OpenSubtitlesPass,
		)
	}
	return &SubtitleService{
		openSubtitles: openSubtitles,
		httpClient:    httpclient.New(),
		cache:         util.NewBoundedCache[model.SubtitleTrack](subtitleCacheSize),
	}
}

// Fetch picks and validates one subtitle track for resolved media, caching
// results by media, language, and resolver.
func (s *SubtitleService) Fetch(
	ctx context.Context,
	media model.ResolvedMedia,
	preferredLang string,
	preferredResolver string,
) (model.SubtitleTrack, error) {
	return s.fetch(ctx, media, preferredLang, preferredResolver, "")
}

// FetchForSource selects a subtitle for one exact playback source so tracks
// from another source handled by the same provider cannot be reused.
func (s *SubtitleService) FetchForSource(
	ctx context.Context,
	media model.ResolvedMedia,
	preferredLang string,
	preferredResolver string,
	preferredSourceID string,
) (model.SubtitleTrack, error) {
	return s.fetch(ctx, media, preferredLang, preferredResolver, preferredSourceID)
}

func (s *SubtitleService) fetch(
	ctx context.Context,
	media model.ResolvedMedia,
	preferredLang string,
	preferredResolver string,
	preferredSourceID string,
) (model.SubtitleTrack, error) {
	ctx, cancel := context.WithTimeout(ctx, subtitleFetchTimeout)
	defer cancel()
	cacheVersion := s.cacheVersion.Load()

	preferredLang = lang.Normalize(preferredLang)
	if preferredLang == "off" {
		return model.SubtitleTrack{}, nil
	}
	if preferredLang == "" {
		preferredLang = "en"
	}

	isAnime := media.ContentMode == provider.ModeAnime || media.MediaType == provider.MediaTypeAnime
	if isAnime && s.areAnimeSubtitlesDisabled() {
		return model.SubtitleTrack{}, nil
	}

	mediaTitle := strings.TrimSpace(media.SeriesTitle)
	if mediaTitle == "" {
		mediaTitle = strings.TrimSpace(media.EpisodeTitle)
	}
	cacheKey := fmt.Sprintf(
		"%d:%d:%d:%s:%s:%s:%s:%s:%s:%d",
		media.TMDBID,
		media.SeasonNumber,
		media.EpisodeNumber,
		mediaTitle,
		preferredLang,
		preferredResolver,
		preferredSourceID,
		media.MediaType,
		media.ContentMode,
		cacheVersion,
	)
	if track, ok := s.cache.Get(cacheKey); ok && usableCachedTrack(track) {
		return track, nil
	}

	originalSubtitles := media.Subtitles
	matchingCandidates := selectMatchingProviderCandidates(
		originalSubtitles,
		preferredLang,
		preferredResolver,
		preferredSourceID,
	)

	attempted := make(map[string]struct{})
	var failures []error

	// Priority order:
	// 1. Source's own provider candidates (matching resolver)
	// 2. OpenSubtitles (dedicated subtitle fallback when active provider has no subs)
	// 3. Other providers' candidates (web stream rips matching the video encode)
	if len(matchingCandidates) > 0 {
		track, err := s.downloadProviderCandidates(ctx, media, matchingCandidates, attempted)
		if err == nil && usableCachedTrack(track) {
			s.cache.Set(cacheKey, track)
			return track, nil
		}
		if err != nil {
			failures = append(failures, err)
		}
	}

	if !isAnime && subtitleFallbackReady(media) && s.openSubtitles != nil && s.openSubtitles.Configured() {
		track, found, err := s.openSubtitles.FetchBestSubtitle(
			ctx,
			mediaTitle,
			preferredLang,
			media.TMDBID,
			media.SeasonNumber,
			media.EpisodeNumber,
		)
		if err != nil {
			failures = append(failures, fmt.Errorf("opensubtitles: %w", err))
		} else if found && usableCachedTrack(track) {
			s.cache.Set(cacheKey, track)
			return track, nil
		}
	}

	otherCandidates := selectOtherProviderCandidates(originalSubtitles, preferredLang, preferredResolver)
	if subtitleFallbackReady(media) && len(otherCandidates) > 0 {
		track, err := s.downloadProviderCandidates(ctx, media, otherCandidates, attempted)
		if err == nil && usableCachedTrack(track) {
			s.cache.Set(cacheKey, track)
			return track, nil
		}
		if err != nil {
			failures = append(failures, err)
		}
	}

	if len(failures) == 0 {
		return model.SubtitleTrack{}, errors.New("no subtitles found")
	}
	return model.SubtitleTrack{}, fmt.Errorf("no subtitles found: %w", errors.Join(failures...))
}

func subtitleFallbackReady(media model.ResolvedMedia) bool {
	return media.TotalResolvers <= 0 || len(media.CompletedResolvers) >= media.TotalResolvers
}

func (s *SubtitleService) areAnimeSubtitlesDisabled() bool {
	s.disableMu.RLock()
	disabled := s.disableAnime
	s.disableMu.RUnlock()
	return disabled
}

func usableCachedTrack(track model.SubtitleTrack) bool {
	path := strings.TrimSpace(track.Path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func selectMatchingProviderCandidates(
	tracks []model.SubtitleTrack,
	preferredLang string,
	preferredResolver string,
	preferredSourceID string,
) []model.SubtitleTrack {
	if preferredResolver == "" {
		return nil
	}
	preferredLang = lang.Normalize(preferredLang)
	var exact, english []model.SubtitleTrack
	for _, track := range tracks {
		if !strings.EqualFold(track.Resolver, preferredResolver) {
			continue
		}
		if preferredSourceID != "" && track.SourceID != preferredSourceID {
			continue
		}
		trackLang := lang.Normalize(track.Language)
		switch {
		case trackLang == preferredLang:
			exact = append(exact, track)
		case preferredLang != "en" && trackLang == "en":
			english = append(english, track)
		}
	}
	return append(defaultFirst(exact), defaultFirst(english)...)
}

func selectOtherProviderCandidates(
	tracks []model.SubtitleTrack,
	preferredLang string,
	preferredResolver string,
) []model.SubtitleTrack {
	preferredLang = lang.Normalize(preferredLang)
	var exact, english []model.SubtitleTrack
	for _, track := range tracks {
		if preferredResolver != "" && strings.EqualFold(track.Resolver, preferredResolver) {
			continue
		}
		trackLang := lang.Normalize(track.Language)
		switch {
		case trackLang == preferredLang:
			exact = append(exact, track)
		case preferredLang != "en" && trackLang == "en":
			english = append(english, track)
		}
	}
	return append(defaultFirst(exact), defaultFirst(english)...)
}

func defaultFirst(tracks []model.SubtitleTrack) []model.SubtitleTrack {
	out := make([]model.SubtitleTrack, 0, len(tracks))
	for _, track := range tracks {
		if track.Default {
			out = append(out, track)
		}
	}
	for _, track := range tracks {
		if !track.Default {
			out = append(out, track)
		}
	}
	return out
}

func (s *SubtitleService) downloadProviderCandidates(
	ctx context.Context,
	media model.ResolvedMedia,
	candidates []model.SubtitleTrack,
	attempted map[string]struct{},
) (model.SubtitleTrack, error) {
	var toProbe []model.SubtitleTrack
	var failures []error

	for _, track := range candidates {
		if len(attempted) >= subtitleProviderCandidateLimit {
			break
		}
		key := subtitleCandidateKey(track)
		if key == "" {
			continue
		}
		if _, ok := attempted[key]; ok {
			continue
		}
		attempted[key] = struct{}{}
		if usableCachedTrack(track) {
			return track, nil
		}
		if strings.TrimSpace(track.URL) == "" {
			failures = append(failures, errors.New("subtitle candidate has no URL"))
			continue
		}
		toProbe = append(toProbe, track)
		if len(toProbe) >= 3 {
			break
		}
	}

	if len(toProbe) == 0 {
		if len(failures) == 0 {
			return model.SubtitleTrack{}, nil
		}
		return model.SubtitleTrack{}, errors.Join(failures...)
	}

	if len(toProbe) == 1 {
		downloadCtx, cancel := context.WithTimeout(ctx, subtitleProviderDownloadTimeout)
		defer cancel()
		downloaded, err := s.downloadProviderSubtitle(downloadCtx, media, toProbe[0])
		if err == nil {
			return downloaded, nil
		}
		failures = append(failures, err)
		return model.SubtitleTrack{}, errors.Join(failures...)
	}

	type subResult struct {
		track model.SubtitleTrack
		err   error
	}

	downloadCtx, cancel := context.WithTimeout(ctx, subtitleProviderDownloadTimeout)
	defer cancel()

	ch := make(chan subResult, len(toProbe))
	for _, track := range toProbe {
		track := track
		go func() {
			downloaded, err := s.downloadProviderSubtitle(downloadCtx, media, track)
			ch <- subResult{track: downloaded, err: err}
		}()
	}

	for range toProbe {
		select {
		case res := <-ch:
			if res.err == nil && usableCachedTrack(res.track) {
				return res.track, nil
			}
			if res.err != nil {
				failures = append(failures, res.err)
			}
		case <-downloadCtx.Done():
			failures = append(failures, downloadCtx.Err())
		}
	}

	if len(failures) == 0 {
		return model.SubtitleTrack{}, nil
	}
	return model.SubtitleTrack{}, errors.Join(failures...)
}

func (s *SubtitleService) downloadProviderSubtitle(
	ctx context.Context,
	media model.ResolvedMedia,
	track model.SubtitleTrack,
) (model.SubtitleTrack, error) {
	cacheDir, err := subtitles.CacheDir()
	if err == nil {
		urlHash := sha256.Sum256([]byte(strings.TrimSpace(track.URL)))
		for _, ext := range []string{".vtt", ".srt", ".ass"} {
			existingPath := filepath.Join(cacheDir, fmt.Sprintf("provider_sub_%x%s", urlHash[:12], ext))
			if info, serr := os.Stat(existingPath); serr == nil && !info.IsDir() && info.Size() > 0 {
				track.Path = existingPath
				track.URL = ""
				return track, nil
			}
		}
	}

	parsed, err := url.Parse(strings.TrimSpace(track.URL))
	if err != nil {
		return model.SubtitleTrack{}, fmt.Errorf("parse subtitle URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return model.SubtitleTrack{}, fmt.Errorf("unsupported subtitle URL scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return model.SubtitleTrack{}, errors.New("subtitle URL has no host")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return model.SubtitleTrack{}, fmt.Errorf("create subtitle request: %w", err)
	}
	source := subtitleParentSource(media.Playback, track.SourceID, track.SourceURL)
	req.Header.Set("User-Agent", sourceUserAgent(source))
	referer := firstNonEmptyTrackValue(track.Referer, source.Referer)
	if referer != "" {
		req.Header.Set("Referer", referer)
		if !source.SuppressOrigin {
			if origin := subtitleOrigin(referer); origin != "" {
				req.Header.Set("Origin", origin)
			}
		}
	}
	if strings.TrimSpace(source.CookieHeader) != "" {
		req.Header.Set("Cookie", source.CookieHeader)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return model.SubtitleTrack{}, fmt.Errorf("download subtitle: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return model.SubtitleTrack{}, fmt.Errorf("download subtitle: status %d", resp.StatusCode)
	}

	data, err := readProviderSubtitle(resp.Body)
	if err != nil {
		return model.SubtitleTrack{}, err
	}
	processed, format, err := subtitles.ProcessSubtitleData(data)
	if err != nil {
		return model.SubtitleTrack{}, fmt.Errorf("validate subtitle: %w", err)
	}

	if cacheDir == "" {
		cacheDir, err = subtitles.CacheDir()
		if err != nil {
			return model.SubtitleTrack{}, fmt.Errorf("subtitle cache dir: %w", err)
		}
	}
	urlHash := sha256.Sum256([]byte(strings.TrimSpace(track.URL)))
	filename := fmt.Sprintf("provider_sub_%x%s", urlHash[:12], subtitles.FileExtension(format))
	path := filepath.Join(cacheDir, filename)
	if err := util.AtomicWriteFile(path, processed, 0o644); err != nil {
		return model.SubtitleTrack{}, fmt.Errorf("write subtitle: %w", err)
	}

	track.Path = path
	track.URL = ""
	return track, nil
}

func readProviderSubtitle(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, httpclient.MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read subtitle: %w", err)
	}
	if len(data) > httpclient.MaxBodyBytes {
		return nil, httpclient.ErrBodyTooLarge
	}
	return data, nil
}

func subtitleParentSource(
	sources []provider.MediaSource,
	sourceID string,
	sourceURL string,
) provider.MediaSource {
	sourceID = strings.TrimSpace(sourceID)
	sourceURL = strings.TrimSpace(sourceURL)
	for _, source := range sources {
		if sourceID != "" && source.TransportIdentity() == sourceID {
			return source
		}
	}
	for _, source := range sources {
		if sourceURL != "" && strings.TrimSpace(source.URL) == sourceURL {
			return source
		}
	}
	return provider.MediaSource{}
}

func sourceUserAgent(source provider.MediaSource) string {
	if userAgent := strings.TrimSpace(source.UserAgent); userAgent != "" {
		return userAgent
	}
	return config.DesktopUserAgent
}

func firstNonEmptyTrackValue(values ...string) string {
	for _, value := range values {
		if clean := strings.TrimSpace(value); clean != "" {
			return clean
		}
	}
	return ""
}

func subtitleOrigin(referer string) string {
	parsed, err := url.Parse(strings.TrimSpace(referer))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func subtitleCandidateKey(track model.SubtitleTrack) string {
	if usableCachedTrack(track) {
		return "path:" + strings.TrimSpace(track.Path)
	}
	if strings.TrimSpace(track.URL) != "" {
		return strings.Join([]string{
			strings.TrimSpace(track.URL),
			strings.TrimSpace(track.SourceID),
			strings.TrimSpace(track.SourceURL),
			strings.TrimSpace(track.Referer),
		}, "\x00")
	}
	return "resolver:" + track.Resolver + ":" + track.Language
}
