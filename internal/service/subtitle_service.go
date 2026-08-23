package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kari/internal/config"
	"kari/internal/httpclient"
	"kari/internal/lang"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/subtitles"
	"kari/internal/util"
)

// log scopes every line from this package/component.
var subSvcLog = logging.With("component", "service.subtitles")

// subtitleCacheSize bounds SubtitleService's in-memory result cache so a
// long session watching many different titles doesn't grow it forever.
const subtitleCacheSize = 100

// subtitleCacheMaxAge is how long downloaded subtitle files are kept on
// disk in internal/subtitles.CacheDir() before being pruned on startup.
const subtitleCacheMaxAge = 7 * 24 * time.Hour

// SubtitleService selects and materializes subtitles: active provider's
// track in preferred language first, then that provider's English, then
// OpenSubtitles, then other providers' tracks — never another language.
type SubtitleService struct {
	openSubtitles *subtitles.Client
	httpClient    *http.Client
	cache         *util.BoundedCache[[]model.SubtitleTrack]
}

// NewSubtitleService builds the service; OpenSubtitles stays unconfigured
// unless credentials are present. A background goroutine prunes stale cache
// files at startup.
func NewSubtitleService(cfg *config.Config) *SubtitleService {
	if cfg == nil {
		cfg = &config.Config{}
	}
	var openSubtitles *subtitles.Client
	if strings.TrimSpace(cfg.OpenSubtitlesKey) != "" && strings.TrimSpace(cfg.OpenSubtitlesUser) != "" && strings.TrimSpace(cfg.OpenSubtitlesPass) != "" {
		openSubtitles = subtitles.NewClient(cfg.OpenSubtitlesKey, cfg.OpenSubtitlesUser, cfg.OpenSubtitlesPass)
	}
	go func() {
		if err := subtitles.PruneCacheDir(subtitleCacheMaxAge); err != nil {
			subSvcLog.Debug("subtitle cache prune failed", "err", err)
		}
	}()
	return &SubtitleService{
		openSubtitles: openSubtitles,
		httpClient:    httpclient.New(),
		cache:         util.NewBoundedCache[[]model.SubtitleTrack](subtitleCacheSize),
	}
}

// Fetch picks and downloads the best subtitle track for resolved media,
// caching results by (media, language, resolver).
func (s *SubtitleService) Fetch(ctx context.Context, media model.ResolvedMedia, preferredLang, preferredResolver string) ([]model.SubtitleTrack, error) {
	preferredLang = lang.Normalize(preferredLang)
	if preferredLang == "" {
		preferredLang = "en"
	}

	cacheKey := fmt.Sprintf("%d:%d:%d:%s:%s:%s", media.TMDBID, media.SeasonNumber, media.EpisodeNumber, media.SeriesTitle, preferredLang, preferredResolver)
	if tracks, ok := s.cache.Get(cacheKey); ok {
		return tracks, nil
	}

	titleKey := fmt.Sprintf("%d:%d:%d:%s:%s", media.TMDBID, media.SeasonNumber, media.EpisodeNumber, media.SeriesTitle, preferredLang)

	originalSubtitles := media.Subtitles
	for i, t := range originalSubtitles {
		subSvcLog.Debug("incoming subtitle candidate", "index", i, "label", t.Label, "language", t.Language, "resolver", t.Resolver, "url", t.URL, "path", t.Path)
	}

	// Priority 1: Subtitles from the MATCHING provider (preferredResolver)
	matchingSubs := selectMatchingProviderCandidates(originalSubtitles, preferredLang, preferredResolver)
	if len(matchingSubs) > 0 {
		subSvcLog.Debug("matching provider subtitles found", "count", len(matchingSubs), "resolver", preferredResolver)
		mCopy := model.ResolvedMedia{Subtitles: matchingSubs}
		if s.downloadProviderSubtitles(ctx, &mCopy) {
			if track, ok := s.pickBestSubtitle(mCopy.Subtitles); ok {
				tracks := []model.SubtitleTrack{track}
				subSvcLog.Debug("selected matching provider sub", "path", track.Path, "lang", track.Language, "resolver", track.Resolver)
				s.cache.Set(cacheKey, tracks)
				s.cache.Set(titleKey, tracks)
				return tracks, nil
			}
		}
	}

	// Fast-path: if the active provider lacks subtitles (e.g. VidKing), reuse an
	// already-downloaded subtitle track for this title/language from a sibling provider.
	if cachedTracks, ok := s.cache.Get(titleKey); ok && len(cachedTracks) > 0 {
		if _, err := os.Stat(cachedTracks[0].Path); err == nil {
			subSvcLog.Debug("reusing title cached subtitle", "path", cachedTracks[0].Path, "lang", cachedTracks[0].Language)
			s.cache.Set(cacheKey, cachedTracks)
			return cachedTracks, nil
		}
	}

	query := strings.TrimSpace(media.SeriesTitle)
	if query == "" {
		query = strings.TrimSpace(media.EpisodeTitle)
	}

	// Priority 2: Try OpenSubtitles
	if s.openSubtitles != nil && s.openSubtitles.Configured() {
		track, found, err := s.openSubtitles.FetchBestSubtitle(ctx, query, preferredLang, media.TMDBID, media.SeasonNumber, media.EpisodeNumber)
		if err == nil && found {
			tracks := []model.SubtitleTrack{track}
			subSvcLog.Debug("selected opensubtitles sub", "path", track.Path, "lang", track.Language)
			s.cache.Set(cacheKey, tracks)
			s.cache.Set(titleKey, tracks)
			return tracks, nil
		}
		if err != nil {
			subSvcLog.Warn("opensubtitles lookup failed", "err", err)
		}
	}

	// Priority 3: Fall back to OTHER providers (only if matching provider and
	// OpenSubtitles had no usable subs)
	otherSubs := selectOtherProviderCandidates(originalSubtitles, preferredLang, preferredResolver)
	if len(otherSubs) > 0 {
		subSvcLog.Debug("falling back to other providers' tracks", "count", len(otherSubs))
		mCopy := model.ResolvedMedia{Subtitles: otherSubs}
		if s.downloadProviderSubtitles(ctx, &mCopy) {
			if track, ok := s.pickBestSubtitle(mCopy.Subtitles); ok {
				tracks := []model.SubtitleTrack{track}
				subSvcLog.Debug("selected fallback other provider sub", "path", track.Path, "lang", track.Language, "resolver", track.Resolver)
				s.cache.Set(cacheKey, tracks)
				s.cache.Set(titleKey, tracks)
				return tracks, nil
			}
		}
	}

	return nil, fmt.Errorf("no subtitles found")
}
// pickBestSubtitle returns the first successfully-downloaded candidate.
// It doesn't need to check language itself — selectSubtitleCandidates has
// already restricted and ordered the list (active provider before others,
// preferred language before English, never any other language), so "first
// downloaded" is already the best available choice.
func (s *SubtitleService) pickBestSubtitle(tracks []model.SubtitleTrack) (model.SubtitleTrack, bool) {
	for _, t := range tracks {
		if t.Path != "" {
			return t, true
		}
	}
	return model.SubtitleTrack{}, false
}

// selectMatchingProviderCandidates returns subtitles from the matched provider (preferredResolver)
// filtered to preferredLang or English.
func selectMatchingProviderCandidates(tracks []model.SubtitleTrack, preferredLang, preferredResolver string) []model.SubtitleTrack {
	if preferredResolver == "" {
		return nil
	}
	preferredLang = lang.Normalize(preferredLang)
	var exact, english []model.SubtitleTrack
	for _, t := range tracks {
		if !strings.EqualFold(t.Resolver, preferredResolver) {
			continue
		}
		tLang := lang.Normalize(t.Language)
		if tLang == preferredLang {
			exact = append(exact, t)
		} else if preferredLang != "en" && tLang == "en" {
			english = append(english, t)
		}
	}
	return append(exact, english...)
}

func selectOtherProviderCandidates(tracks []model.SubtitleTrack, preferredLang, preferredResolver string) []model.SubtitleTrack {
	preferredLang = lang.Normalize(preferredLang)
	var exact, english []model.SubtitleTrack
	for _, t := range tracks {
		if preferredResolver != "" && strings.EqualFold(t.Resolver, preferredResolver) {
			continue
		}
		tLang := lang.Normalize(t.Language)
		if tLang == preferredLang {
			exact = append(exact, t)
		} else if preferredLang != "en" && tLang == "en" {
			english = append(english, t)
		}
	}
	return append(exact, english...)
}

func (s *SubtitleService) downloadProviderSubtitles(ctx context.Context, media *model.ResolvedMedia) bool {
	for i, sub := range media.Subtitles {
		if sub.URL != "" && sub.Path == "" {
			dlCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
			localPath, err := s.downloadProviderSubtitle(dlCtx, sub.URL, sub.Referer)
			cancel()
			if err == nil {
				media.Subtitles[i].Path = localPath
				media.Subtitles[i].URL = ""
				return true
			}
			subSvcLog.Warn("provider subtitle download failed", "url", sub.URL, "err", err)
		} else if sub.Path != "" {
			return true
		}
	}
	return false
}

func (s *SubtitleService) downloadProviderSubtitle(ctx context.Context, subURL string, referer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, subURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.DesktopUserAgent)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty subtitle file")
	}

	processedData, detectedFormat := subtitles.ProcessSubtitleData(data)

	subDir, err := subtitles.CacheDir()
	if err != nil {
		return "", err
	}

	ext := ".srt"
	if detectedFormat == "ass" || strings.HasSuffix(strings.ToLower(subURL), ".ass") {
		ext = ".ass"
	} else if detectedFormat == "vtt" {
		ext = ".vtt"
	}

	filename := fmt.Sprintf("provider_sub_%d%s", time.Now().UnixNano(), ext)
	localPath := filepath.Join(subDir, filename)

	if err := os.WriteFile(localPath, processedData, 0o644); err != nil {
		return "", err
	}

	return localPath, nil
}
