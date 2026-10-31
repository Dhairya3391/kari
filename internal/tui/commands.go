package tui

import (
	"context"
	"fmt"
	"kari/internal/downloader"
	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/service"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *modelImpl) searchCmd(opID int, query string) tea.Cmd {
	if m.searchQueryCancel != nil {
		m.searchQueryCancel()
		m.searchQueryCancel = nil
	}
	parent := m.appCtx
	if parent == nil {
		parent = context.Background()
	}
	searchCtx, cancel := context.WithCancel(parent)
	m.searchQueryCancel = cancel

	mode := m.appMode
	cacheable := !m.modeFeatures().NoCachedSearches
	return func() tea.Msg {
		cacheKey := fmt.Sprintf("%s:%s", mode, query)
		if cacheable {
			if entry, ok := m.searchCache.Get(cacheKey); ok {
				logging.Debug("search cache hit", "mode", mode, "query", query)
				return searchDoneMsg{results: entry.results, usedQuery: entry.usedQuery, warnings: entry.warnings, opID: opID, err: nil}
			}
		}

		logging.Debug("search start", "mode", mode, "query", query)
		results, usedQuery, warnings, err := m.mediaService.Search(searchCtx, mode, query)
		if err == nil && cacheable && len(warnings) == 0 {
			m.searchCache.Set(cacheKey, searchCacheEntry{
				results:   results,
				usedQuery: usedQuery,
				warnings:  warnings,
			})
		}

		return searchDoneMsg{results: results, usedQuery: usedQuery, warnings: warnings, opID: opID, err: err}
	}
}

func (m *modelImpl) episodesCmd(opID int, series provider.SearchResult) tea.Cmd {
	mode := m.appMode
	audioMode := m.audioMode
	return func() tea.Msg {
		results, err := m.mediaService.FetchEpisodes(m.appCtx, mode, series, audioMode)
		return episodesDoneMsg{results: results, opID: opID, err: err}
	}
}

func (m *modelImpl) historyContinueEpisodesCmd(opID int, group history.Group, series provider.SearchResult, mode provider.ContentType) tea.Cmd {
	audioMode := m.audioMode
	return func() tea.Msg {
		results, err := m.mediaService.FetchEpisodes(m.appCtx, mode, series, audioMode)
		return historyContinueEpisodesMsg{group: group, results: results, opID: opID, err: err}
	}
}

// resolveCmd resolves sources, skipping exclude providers that already
// delivered: retries only ask the failed ones again, never the
// successful. Fresh loads pass nil.
func (m *modelImpl) resolveCmd(opID int, series provider.SearchResult, episode provider.Episode, exclude, retry []string) tea.Cmd {
	tuiLog.Debug("resolve starting", "opID", opID, "series", series.Title, "episode", episode.Title, "exclude", exclude, "retry", retry)
	mode := m.appMode

	return tea.Batch(
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.appCtx, 60*time.Second)
			defer cancel()

			onResult := func(resolved model.ResolvedMedia) {
				select {
				case m.resolveChan <- resolveProgressMsg{resolved: resolved, opID: opID}:
				case <-ctx.Done():
				}
			}

			resolved, err := m.mediaService.Resolve(ctx, mode, series, episode, onResult, service.ResolveOptions{Exclude: exclude, Retry: retry})

			// Deliver the completion marker through the same channel the
			// subscription reads (mirroring download/batch) so the
			// subscription goroutine that consumes it terminates instead of
			// blocking forever on <-resolveChan after the last progress
			// message. Returning the marker directly here would orphan that
			// goroutine, and stale ones would then steal the first message
			// of the next resolve.
			select {
			case m.resolveChan <- resolveDoneMsg{resolved: resolved, opID: opID, err: err}:
			case <-ctx.Done():
			}

			return resolveWorkerDoneMsg{}
		},
		m.resolveSubscription(),
	)
}

func (m *modelImpl) invalidateSubtitleSync() {
	m.subtitleOpID = 0
	m.subtitleResolverUsed = ""
	m.subtitleLangUsed = ""
	m.subtitleSourceUsed = ""
	m.subtitlePoolSize = 0
	m.subtitlePoolComplete = false
	if m.resolved != nil {
		m.resolved.SelectedSubtitle = nil
	}
}

// triggerSubtitleSync fetches subtitles in the background, starting on
// the first resolve progress snapshot rather than waiting for every
// provider: play starts from sources alone and the track attaches when
// it arrives (onSubtitleDone). A grown subtitle pool re-triggers the
// fetch while nothing usable came out of the earlier partial pool yet,
// so late provider tracks still get tried.
func (m *modelImpl) triggerSubtitleSync() tea.Cmd {
	if m.resolved == nil {
		return nil
	}
	if !m.subtitlesWanted() {
		m.invalidateSubtitleSync()
		return nil
	}
	if m.subtitleService == nil {
		return nil
	}
	src, ok := m.selectedPlaybackSource()
	if !ok {
		return nil
	}

	hasSecuredProviderTrack := m.resolved.SelectedSubtitle != nil && strings.TrimSpace(m.resolved.SelectedSubtitle.SourceID) != ""
	sourceOrTargetChanged := src.Resolver != m.subtitleResolverUsed ||
		m.subtitleLanguage != m.subtitleLangUsed ||
		src.URL != m.subtitleSourceUsed
	targetChanged := sourceOrTargetChanged ||
		(subtitleResolutionComplete(*m.resolved) && !m.subtitlePoolComplete && !hasSecuredProviderTrack) ||
		(len(m.rawSubtitles) > m.subtitlePoolSize && m.subtitleOpID == 0 && !hasSecuredProviderTrack)
	if m.subtitleOpID != 0 && !sourceOrTargetChanged {
		return nil
	}
	if !targetChanged {
		return nil
	}

	m.subtitleOpID = 0
	if sourceOrTargetChanged {
		m.resolved.SelectedSubtitle = nil
	}
	m.subtitleResolverUsed = src.Resolver
	m.subtitleLangUsed = m.subtitleLanguage
	m.subtitleSourceUsed = src.URL
	m.subtitlePoolSize = len(m.rawSubtitles)
	m.subtitlePoolComplete = subtitleResolutionComplete(*m.resolved)
	opID := m.newOpID()
	m.subtitleOpID = opID
	mediaForFetch := *m.resolved
	mediaForFetch.Subtitles = append([]model.SubtitleTrack{}, m.rawSubtitles...)
	return m.subtitleFetchCmd(opID, mediaForFetch, src.Resolver, src.TransportIdentity())
}

func subtitleResolutionComplete(media model.ResolvedMedia) bool {
	return media.TotalResolvers <= 0 || len(media.CompletedResolvers) >= media.TotalResolvers
}

func (m *modelImpl) subtitleFetchCmd(opID int, resolved model.ResolvedMedia, preferredResolver, preferredSourceID string) tea.Cmd {
	preferredLang := m.subtitleLanguage
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.appCtx, 15*time.Second)
		defer cancel()
		track, err := m.subtitleService.FetchForSource(ctx, resolved, preferredLang, preferredResolver, preferredSourceID)
		if err != nil {
			tuiLog.Warn("subtitle fetch failed", "err", err)
		}
		return subtitleDoneMsg{track: track, opID: opID, err: err}
	}
}

func (m *modelImpl) playCmd(opID int) tea.Cmd {
	if m.resolved == nil {
		return m.playCmdWithStartTime(opID, 0)
	}
	return m.playCmdWithStartTime(opID, m.resolved.StartTime)
}

func (m *modelImpl) playCmdWithStartTime(opID int, startTime float64) tea.Cmd {
	sources := m.orderedPlaybackSources()
	if m.resolved == nil || len(sources) == 0 {
		return func() tea.Msg {
			return playDoneMsg{opID: opID, err: fmt.Errorf("no playback source matches the current filters")}
		}
	}
	resolved := *m.resolved
	resolved.StartTime = startTime
	resolved.SubtitleLanguage = m.subtitleLanguage
	playerName := m.selectedPlayerName()
	providerName := ""
	if src, ok := m.selectedPlaybackSource(); ok {
		providerName = src.Resolver
		if providerName == "" {
			providerName = src.Quality
		}
	}
	// Built on the Update goroutine (reads model state); the closure below
	// only touches the thread-safe presence client.
	nowPlaying, publishPresence := m.nowPlayingFor(resolved)
	return func() tea.Msg {
		if publishPresence {
			m.publishPresence(nowPlaying)
		}
		tuiLog.Debug("play starting", "opID", opID, "media", resolved.DisplayTitle(), "provider", providerName, "sourcesCount", len(sources), "startTime", startTime)
		tuiLog.Debug("launching playback", "media", resolved.DisplayTitle(), "player", playerName, "hasSubtitles", resolved.SubtitlePath() != "")
		result, err := m.players.PlayWithSources(sources, resolved, playerName)
		return playDoneMsg{opID: opID, provider: providerName, result: result, err: err}
	}
}

func (m *modelImpl) downloadCmd(opID int, resolved model.ResolvedMedia) tea.Cmd {
	if len(resolved.Subtitles) == 0 && len(m.rawSubtitles) > 0 {
		resolved.Subtitles = append([]model.SubtitleTrack{}, m.rawSubtitles...)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(m.appCtx)
		outputDir, title := m.downloadService.OrganizedPath(resolved)

		go func() {
			defer cancel()
			err := m.downloadService.Download(ctx, resolved, func(dp downloader.DownloadProgress) {
				select {
				case m.downloadChan <- downloadProgressMsg{
					opID:       opID,
					progress:   dp.Percent,
					totalSize:  dp.TotalSize,
					speed:      dp.Speed,
					downloaded: dp.Downloaded,
					eta:        dp.ETA,
				}:
				default:
				}
			})
			// Unlike progress ticks above, the completion message must
			// never be dropped: nothing else clears m.loading or
			// re-enables starting another download, so a non-blocking
			// send here (channel full from a backlog of progress
			// updates the UI hasn't drained yet) would leave the app
			// stuck showing "Downloading..." forever. It only sends
			// once per download, so blocking briefly is harmless. The
			// ctx.Done() case only fires if the user explicitly
			// cancelled (Stop/Quit) — in that case the UI already reset
			// itself synchronously, nobody will read this value, and
			// without this escape the goroutine would block forever.
			select {
			case m.downloadChan <- downloadDoneMsg{opID: opID, err: err}:
			case <-ctx.Done():
			}
		}()

		return downloadStartedMsg{
			opID:      opID,
			cancel:    cancel,
			outputDir: outputDir,
			title:     title,
		}
	}
}

func (m *modelImpl) resumePendingDownloadCmd(opID int, job service.DownloadJob) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(m.appCtx)
		meta := model.ResolvedMedia{
			SeriesTitle: job.Series.Title, EpisodeTitle: job.Episode.Title,
			MediaType: job.Series.MediaType, Year: job.Series.Year,
			TMDBID: job.Series.TMDBID, SeasonNumber: job.Episode.Season,
			EpisodeNumber: job.Episode.Episode,
		}
		outputDir, title := m.downloadService.OrganizedPath(meta)
		go func() {
			defer cancel()
			if job.SearchOnResume {
				results, _, _, err := m.mediaService.Search(ctx, job.Mode, job.Series.Title)
				if err == nil {
					wanted := strings.ToLower(strings.Join(strings.Fields(job.Series.Title), " "))
					matched := false
					for _, result := range results {
						if strings.ToLower(strings.Join(strings.Fields(result.Title), " ")) == wanted {
							job.Series = result
							job.SearchOnResume = false
							matched = true
							_ = m.downloadService.SavePendingJob(job)
							break
						}
					}
					if !matched {
						err = fmt.Errorf("could not find an exact provider match for %q", job.Series.Title)
					}
				}
				if err != nil {
					select {
					case m.downloadChan <- downloadDoneMsg{opID: opID, err: err}:
					case <-ctx.Done():
					}
					return
				}
			}
			resolved, err := m.mediaService.Resolve(ctx, job.Mode, job.Series, job.Episode, nil, service.ResolveOptions{})
			if err == nil {
				err = m.downloadService.Download(ctx, resolved, func(dp downloader.DownloadProgress) {
					select {
					case m.downloadChan <- downloadProgressMsg{opID: opID, progress: dp.Percent, totalSize: dp.TotalSize, speed: dp.Speed, downloaded: dp.Downloaded, eta: dp.ETA}:
					default:
					}
				})
			}
			select {
			case m.downloadChan <- downloadDoneMsg{opID: opID, err: err}:
			case <-ctx.Done():
			}
		}()
		return downloadStartedMsg{opID: opID, cancel: cancel, outputDir: outputDir, title: title}
	}
}

func (m *modelImpl) playDownloadedFileCmd(opID int, filePath, title string) tea.Cmd {
	playerName := m.selectedPlayerName()
	return func() tea.Msg {
		sources := []provider.MediaSource{{URL: filePath}}
		resolved := model.ResolvedMedia{
			SeriesTitle: title,
			Playback:    sources,
		}
		result, err := m.players.PlayWithSources(sources, resolved, playerName)
		return playDoneMsg{opID: opID, provider: "local", result: result, err: err}
	}
}
