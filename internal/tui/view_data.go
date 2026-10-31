package tui

import (
	"fmt"
	"kari/internal/history"
	"kari/internal/lang"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/service"
	"kari/internal/termimg"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *modelImpl) collectSearchData(width int, accent lipgloss.AdaptiveColor) SearchData {
	inputView := "› search " + strings.ToLower(string(m.appMode)) + "…"
	if m.queryInput.Focused() {
		inputView = m.queryInput.View()
	}

	return SearchData{
		Modes:        m.modes,
		ActiveMode:   m.appMode,
		InputView:    inputView,
		InputFocused: m.queryInput.Focused(),
		Loading:      m.loading,
		LoadingText:  m.loadingText,
		SpinnerFrame: m.spinner.View(),
		Width:        width,
		Accent:       accent,
	}
}

func (m *modelImpl) collectResultsData(width, height int, accent lipgloss.AdaptiveColor) ResultsData {
	posterBlock := posterBlock(m.searchPoster, m.searchPosterUnavailable, m.imgProtocol, kittySearchImageID, m.imagesEnabled)

	// The / filter narrows the widget, so the highlight is the widget
	// position — not the original index, which only selection uses.
	visible, _ := m.visibleSeriesResults()
	pos := m.seriesList.Index()
	if pos < 0 || pos >= len(visible) {
		pos = 0
	}

	inputView, inputFocused := "", false
	if m.queryInput.Focused() {
		inputView, inputFocused = m.queryInput.View(), true
	}

	return ResultsData{
		Query:         m.searchQuery,
		InputView:     inputView,
		InputFocused:  inputFocused,
		Mode:          m.appMode,
		Results:       visible,
		SelectedIndex: pos,
		FilterQuery:   m.resultsFilter,
		Filtering:     m.resultsFiltering,
		PosterBlock:   posterBlock,
		Overview:      m.previewOverview,
		Genres:        m.previewGenres,
		Rating:        m.previewRating,
		Loading:       m.loading,
		LoadingText:   m.loadingText,
		SpinnerFrame:  m.spinner.View(),
		Width:         width,
		TermWidth:     m.width,
		Height:        height,
		Accent:        accent,
	}
}

func (m *modelImpl) collectEpisodesData(width, height int, accent lipgloss.AdaptiveColor) EpisodesData {
	seriesTitle := "Episodes"
	if m.selectedSeries != nil && m.selectedSeries.Title != "" {
		seriesTitle = m.selectedSeries.Title
	}

	seasonEpisodes, episodeIndices := m.currentSeasonEpisodes()
	seasons := m.episodeSeasons()
	seasonCount := len(seasons)
	if seasonCount <= 0 {
		seasonCount = 1
	}

	seriesYear := ""
	seriesKind := ""
	if m.selectedSeries != nil {
		seriesYear = m.selectedSeries.Year
		seriesKind = m.selectedSeries.MediaType
	}

	selectedEpTitle := ""
	if m.seasonEpisodeIndex >= 0 && m.seasonEpisodeIndex < len(seasonEpisodes) {
		ep := seasonEpisodes[m.seasonEpisodeIndex]
		num := ep.Episode
		if num == 0 {
			num = m.seasonEpisodeIndex + 1
		}
		title := ep.Title
		if title == "" {
			title = fmt.Sprintf("Episode %d", num)
		}
		selectedEpTitle = fmt.Sprintf("%02d · %s", num, title)
	}

	return EpisodesData{
		SeriesTitle:          seriesTitle,
		Episodes:             m.episodeResults,
		filteredEpisodes:     seasonEpisodes,
		episodeIndices:       episodeIndices,
		episodeListReady:     true,
		HistoryIndex:         m.episodeHistoryIndex(seriesTitle),
		SelectedIndex:        m.seasonEpisodeIndex,
		SeasonCount:          seasonCount,
		ActiveSeason:         m.activeSeason,
		SelectMode:           m.selectMode,
		SelectedIDs:          m.selectedEpisodes,
		FilterQuery:          m.episodeFilter,
		Filtering:            m.episodeFiltering,
		Loading:              m.loading,
		LoadingText:          m.loadingText,
		SpinnerFrame:         m.spinner.View(),
		Width:                width,
		Height:               height,
		Accent:               accent,
		PosterBlock:          posterBlock(m.searchPoster, m.searchPosterUnavailable, m.imgProtocol, kittySearchImageID, m.imagesEnabled),
		SeriesYear:           seriesYear,
		SeriesKind:           seriesKind,
		SeriesRating:         m.previewRating,
		SeriesGenres:         m.previewGenres,
		SeriesOverview:       m.previewOverview,
		TermWidth:            m.width,
		SelectedEpisodeTitle: selectedEpTitle,
	}
}

func (m *modelImpl) episodeHistoryIndex(seriesTitle string) history.EpisodeIndex {
	cache := &m.episodeHistory
	if cache.valid && cache.title == seriesTitle {
		return cache.index
	}
	cache.title = seriesTitle
	cache.index = nil
	if m.historyStore != nil {
		cache.index = history.BuildEpisodeIndex(m.historyStore.All(), seriesTitle)
	}
	cache.valid = true
	return cache.index
}

func (m *modelImpl) invalidateEpisodeHistoryIndex() {
	m.episodeHistory.valid = false
}
func (m *modelImpl) collectChaptersData(width, height int, accent lipgloss.AdaptiveColor) ChaptersData {
	title := "Chapters"
	overview := ""
	var genres []string
	providerName := ""
	if m.selectedSeries != nil {
		if m.selectedSeries.Title != "" {
			title = m.selectedSeries.Title
		}
		overview = m.selectedSeries.Overview
		genres = m.selectedSeries.Genres
		providerName = m.registry.DisplayName(selectedSeriesProvider(m.selectedSeries))
	}

	listH := max(3, height-7)
	m.chapterList.SetSize(max(20, width-4), listH)

	return ChaptersData{
		SeriesTitle:   title,
		Genres:        genres,
		ProviderName:  providerName,
		ChaptersCount: len(m.chapters),
		Overview:      overview,
		ListView:      m.chapterList.View(),
		Width:         width,
		Height:        height,
		Accent:        accent,
	}
}

func (m *modelImpl) collectPreviewData(width, height int, accent lipgloss.AdaptiveColor) PreviewData {
	seriesTitle := "Preview"
	if m.selectedSeries != nil && m.selectedSeries.Title != "" {
		seriesTitle = m.selectedSeries.Title
	} else if m.resolved != nil && m.resolved.SeriesTitle != "" {
		seriesTitle = m.resolved.SeriesTitle
	}

	epInfo := ""
	if m.selectedEpisode != nil && (m.selectedEpisode.Season > 0 || m.selectedEpisode.Episode > 0) {
		epTitle := m.selectedEpisode.Title
		if epTitle == "" {
			epTitle = fmt.Sprintf("Episode %d", m.selectedEpisode.Episode)
		}
		totalStr := ""
		if len(m.episodeResults) > 0 {
			totalStr = fmt.Sprintf("  (%d of %d)", m.selectedEpisodeIndex()+1, len(m.episodeResults))
		}
		// Compute available metadata width so the episode title can be
		// truncated before the (X of Y) count is appended. This prevents
		// the combined line from exceeding the screen width, which would
		// cause Bubble Tea to skip the end-of-line erase and leave old
		// image cells visible — the root cause of issue #35.
		metaW := max(20, width-previewPosterMaxCols-4)
		if m.width < 80 && width < 70 {
			metaW = max(20, width)
		}
		var prefix string
		if m.selectedEpisode.Season > 0 {
			prefix = fmt.Sprintf("S%02d E%02d · ", m.selectedEpisode.Season, m.selectedEpisode.Episode)
		} else {
			prefix = fmt.Sprintf("EP %02d · ", m.selectedEpisode.Episode)
		}
		availTitleW := metaW - lipgloss.Width(prefix) - lipgloss.Width(totalStr)
		if availTitleW > 10 && lipgloss.Width(epTitle) > availTitleW {
			epTitle = truncate(epTitle, availTitleW)
		}
		epInfo = prefix + epTitle + totalStr
	}

	audioText := ""
	audioTrack := ""
	if m.appMode == provider.ModeAnime {
		mode := strings.ToLower(strings.TrimSpace(m.audioMode))
		if mode == "" {
			mode = "sub"
		}
		audioTrack = mode
		if mode == "dub" {
			audioText = "dub · English"
		} else {
			audioText = "sub · Japanese"
		}
	}
	// Non-anime modes show per-source audio in the sources table below,
	// so no header audio row is needed here.

	subtitlesText := ""
	subtitleType := ""
	subtitleProvider := ""
	subLang := ""
	if m.resolved != nil && m.resolved.SelectedSubtitle != nil {
		subLang = lang.Normalize(m.resolved.SelectedSubtitle.Language)
	}
	if subLang == "" {
		subLang = m.subtitleLangUsed
	}
	if subLang == "" && m.resolved != nil && len(m.rankedSources) > 0 && m.subtitleLanguage != "off" {
		subLang = m.subtitleLanguage
		if subLang == "" {
			subLang = "en"
		}
	}
	if subLang != "" && subLang != "off" {
		subtitlesText = subLang
		if src, ok := m.selectedPlaybackSource(); ok {
			subtitleType = detectSubtitleType(src, m.resolved, m.rawSubtitles, m.appMode)
			if m.resolved != nil && m.resolved.SubtitlePath() != "" {
				subtitlesText += " ✓"
				if m.resolved.SelectedSubtitle != nil {
					subtitleProvider = m.subtitleProviderName(*m.resolved.SelectedSubtitle)
				}
			}
		}
	}
	resumePos := m.currentResumePosition()

	poster := posterBlock(m.previewPoster, m.previewPosterUnavailable, m.imgProtocol, kittyPreviewImageID, m.imagesEnabled)

	var sources []provider.MediaSource
	if m.resolved != nil {
		sources = m.resolved.Playback
	}

	// Tripwire: ranked must never be empty while resolved sources exist
	// (the tier filter falls back to the full list). If this ever fires,
	// the qualities below identify the unhandled label shape.
	if len(m.rankedSources) == 0 && len(sources) > 0 {
		qualities := make([]string, 0, len(sources))
		for _, s := range sources {
			qualities = append(qualities, s.Quality)
		}
		tuiLog.Warn("preview desync: ranked empty with sources present",
			"qualityMode", m.qualityMode, "sources", len(sources), "qualities", strings.Join(qualities, "|"))
	}

	// Fall back to search-stamped metadata while the async details
	// fetch is in flight (or when it failed): providers like WeebCentral
	// ship overview/genres at search time.
	overview := m.previewOverview
	genres := m.previewGenres
	if overview == "" && m.selectedSeries != nil {
		overview = m.selectedSeries.Overview
	}
	if len(genres) == 0 && m.selectedSeries != nil {
		genres = m.selectedSeries.Genres
	}

	providers := make(map[string]string, len(m.rankedSources))
	for _, scored := range m.rankedSources {
		src := scored.Source
		if _, ok := providers[src.Resolver]; !ok {
			providers[src.Resolver] = m.registry.DisplayName(src.Resolver)
		}
	}
	skipInfo := m.previewSkipInfo
	if src, ok := m.selectedPlaybackSource(); ok {
		if !strings.EqualFold(m.skipProvider, "off") && !strings.EqualFold(m.skipProvider, "none") &&
			src.SkipTimes != nil && src.SkipTimes.HasSegments() {
			skipInfo = strings.TrimSpace(src.SkipTimes.Source)
			if skipInfo == "" {
				skipInfo = m.registry.DisplayName(src.Resolver)
			}
		}
	}

	return PreviewData{
		SeriesTitle:      seriesTitle,
		EpisodeInfo:      epInfo,
		Genres:           genres,
		Rating:           m.previewRating,
		Overview:         overview,
		AudioText:        audioText,
		AudioTrack:       audioTrack,
		SubtitlesText:    subtitlesText,
		SubtitleType:     subtitleType,
		SubtitleProvider: subtitleProvider,
		ResumePosition:   resumePos,
		PosterBlock:      poster,
		Sources:          sources,
		RankedSources:    m.rankedSources,
		ProviderName:     providers,
		SelectedIndex:    m.previewSelectedIndex,
		PlayerName:       m.selectedPlayerName(),
		Autoplay:         m.autoplay,
		SkipInfo:         skipInfo,
		LoadingProviders: m.loadingProviders,
		TotalProviders:   m.totalProviders,
		FailedProvider:   m.failedProviderName,
		SpinnerFrame:     m.spinner.View(),
		Loading:          m.loading,
		Width:            width,
		TermWidth:        m.width,
		Height:           height,
		Accent:           accent,
		Mode:             m.appMode,
	}
}

// detectSubtitleType determines the subtitle kind ("hard subs" or "soft subs")
// for the selected playback source and media context; returns empty string if unknown.
func detectSubtitleType(src provider.MediaSource, resolved *model.ResolvedMedia, rawSubtitles []model.SubtitleTrack, mode provider.ContentType) string {
	if mode != provider.ModeAnime {
		return ""
	}
	// A declared subtitle kind wins over heuristics: a hard-subtitled
	// stream stays "hard subs" even when the pool also lists
	// downloadable sidecar tracks.
	switch strings.ToLower(strings.TrimSpace(src.SubType)) {
	case provider.SubTypeHard:
		return "hard subs"
	case provider.SubTypeSoft:
		return "soft subs"
	}
	if len(src.Subtitles) > 0 {
		return "soft subs"
	}
	if (resolved != nil && len(resolved.Subtitles) > 0) || len(rawSubtitles) > 0 {
		return "soft subs"
	}
	return ""
}

func (m *modelImpl) subtitleProviderName(track model.SubtitleTrack) string {
	if name := strings.TrimSpace(track.ProviderName); name != "" {
		return name
	}
	resolver := strings.TrimSpace(track.Resolver)
	if resolver != "" {
		if m.registry != nil {
			displayName := m.registry.DisplayName(resolver)
			if displayName != "" && !strings.EqualFold(displayName, resolver) {
				return displayName
			}
			if displayName != "" {
				if len(displayName) > 0 && displayName[0] >= 'a' && displayName[0] <= 'z' {
					return strings.ToUpper(displayName[:1]) + displayName[1:]
				}
				return displayName
			}
		}
		if len(resolver) > 0 && resolver[0] >= 'a' && resolver[0] <= 'z' {
			return strings.ToUpper(resolver[:1]) + resolver[1:]
		}
		return resolver
	}
	return ""
}

func (m *modelImpl) collectHistoryData(width, height int, accent lipgloss.AdaptiveColor) HistoryData {
	continued, finished := splitHistoryGroups(m.historyGroups)

	activeTab := HistoryTabContinue
	finishedTab := m.historyTab == historyTabFinished
	activeList := sortGroupsByDateBucket(continued, false)
	if finishedTab {
		activeTab = HistoryTabFinished
		activeList = sortGroupsByDateBucket(finished, true)
	}
	modeFilters := historyModeFilters(m.historyGroups, activeList)
	activeList = filterHistoryGroups(activeList, m.historyModeFilter)
	activeList = searchHistoryGroups(activeList, m.historyFilter)
	poster := ""
	if m.imagesEnabled {
		if m.imgProtocol == termimg.ProtocolKitty && m.historyPosterDrawn && m.historyPoster != "" {
			poster = "\u200b" + strings.Repeat(" ", historyPosterMaxCols-1) + strings.Repeat("\n", historyPosterMaxRows-1)
		} else {
			poster = posterBlock(m.historyPoster, m.historyPosterUnavailable, m.imgProtocol, kittyHistoryImageID, m.imagesEnabled)
		}
	}

	return HistoryData{
		ActiveTab:        activeTab,
		ContinueCount:    len(filterHistoryGroups(continued, m.historyModeFilter)),
		FinishedCount:    len(filterHistoryGroups(finished, m.historyModeFilter)),
		ModeFilter:       m.historyModeFilter,
		ModeFilters:      modeFilters,
		FilterQuery:      m.historyFilter,
		Filtering:        m.historyFiltering,
		Groups:           activeList,
		SelectedIndex:    m.historySelectedIndex(),
		PosterBlock:      poster,
		ConfirmClearAll:  m.confirmClearHistory,
		ConfirmDeleteOne: m.confirmDelete,
		Width:            width,
		Height:           height,
		Accent:           accent,
	}
}

func (m *modelImpl) collectSettingsData(width, height int) SettingsData {
	accent := m.currentAccent()

	// Build audio languages summary
	var enabledSummary []string
	var allLangNames []string
	enabledMap := make(map[string]bool)
	allLangs := m.availableLanguages()
	for _, l := range allLangs {
		allLangNames = append(allLangNames, l.Display)
		// Absent from map or true = enabled; false = explicitly disabled
		isDisabled := false
		if val, ok := m.languageFilter[l.Code]; ok && !val {
			isDisabled = true
		} else if val, ok := m.languageFilter[l.Display]; ok && !val {
			isDisabled = true
		}
		if !isDisabled {
			enabledSummary = append(enabledSummary, l.Display)
			enabledMap[l.Display] = true
		}
	}

	audioSummary := "All"
	if len(enabledSummary) == 0 {
		audioSummary = "None"
	} else if len(enabledSummary) < len(allLangs) {
		if len(enabledSummary) <= 3 {
			audioSummary = strings.Join(enabledSummary, " · ")
		} else {
			audioSummary = fmt.Sprintf("%s · %s · %s +%d", enabledSummary[0], enabledSummary[1], enabledSummary[2], len(enabledSummary)-3)
		}
	}

	animeAudio := "Sub · Japanese"
	if strings.EqualFold(m.audioMode, "dub") {
		animeAudio = "Dub · English"
	}

	qualityName := "All"
	switch m.qualityMode {
	case qualityHighest:
		qualityName = "Highest"
	case qualityDataSaver:
		qualityName = "Data Saver"
	case qualityLowest:
		qualityName = "Lowest"
	}

	var downloadQualityName string
	switch m.downloadQuality {
	case downloadQuality4K:
		downloadQualityName = "4K (2160p)"
	case downloadQualityFHD:
		downloadQualityName = "FHD (1080p)"
	case downloadQualityHD:
		downloadQualityName = "HD (720p)"
	case downloadQualitySD:
		downloadQualityName = "SD (480p)"
	default:
		downloadQualityName = "Auto (same as stream)"
	}
	accentName := "Auto (per mode)"
	if m.accentIndex > 0 && m.accentIndex < len(accentPresets) {
		accentName = accentPresets[m.accentIndex].name
	} else if m.customAccentHex != "" {
		accentName = "Custom (" + m.customAccentHex + ")"
	}
	defaultModeName := "Last active"
	curMode := strings.ToLower(strings.TrimSpace(m.defaultMode))
	for _, opt := range m.availableStartupModes() {
		if opt.Key == curMode {
			defaultModeName = opt.Label
			break
		}
	}

	// Build modes rows for Settings > Modes
	var modeRows []ModeRow
	for _, modeKey := range m.configuredModes {
		kind := model.FromKey(modeKey)
		modeThm := ThemeFor(kind, "Auto")
		label := modeKey
		if s := kind.Spec(); s != nil && s.Label != "" {
			label = s.Label
		}
		avail := m.isModeAvailable(modeKey)
		var unavailNote string
		if !avail {
			if modeKey == "jellyfin" {
				unavailNote = "set JELLYFIN_URL"
			} else {
				unavailNote = "no provider"
			}
		}
		modeRows = append(modeRows, ModeRow{
			Key:         modeKey,
			Label:       label,
			Enabled:     !m.disabledModes[modeKey],
			Available:   avail,
			UnavailNote: unavailNote,
			Accent:      modeThm.Accent,
		})
	}

	return SettingsData{
		ActiveCategory:      m.settingsCategory,
		FocusedRowIndex:     m.settingsIndex,
		Width:               width,
		Height:              height,
		Accent:              accent,
		PlayerName:          m.selectedPlayerName(),
		QualityName:         qualityName,
		DownloadQualityName: downloadQualityName,
		Autoplay:            m.autoplay,
		VerifyDownloads:     !m.disableDownloadVerification,
		AudioSummary:        audioSummary,
		SubtitleLanguage:    lang.Name(m.subtitleLanguage),
		AudioPickerOpen:     m.audioPickerOpen,
		AllLanguages:        allLangNames,
		EnabledLanguages:    enabledMap,
		PickerIndex:         m.audioPickerIndex,
		AnimeAudioTrack:     animeAudio,
		AnimeSubtitles:      !m.disableAnimeSubtitles,
		SkipSource:          m.skipProvider,
		AutoSkipIntro:       m.autoSkipIntro,
		AutoSkipEnding:      m.autoSkipEnding,
		AutoSkipRecap:       m.skipRecap,
		AutoSkipPreview:     m.skipPreview,
		AniListConnected:    m.anilistClient != nil && m.anilistClient.IsAuthenticated(),
		AniListAuthActive:   m.anilistAuthURL != "",
		AniListAuthURL:      m.anilistAuthURL,
		AuthInputView:       m.authInput.View(),
		TraktConnected:      m.traktClient != nil && m.traktClient.IsAuthenticated(),
		TraktAuthActive:     m.traktAuthActive,
		TraktUserCode:       m.traktUserCode,
		TraktVerifyURL:      m.traktVerifyURL,
		StartupSync:         m.startupSync,
		DiscordPresence:     m.discordPresence,
		LoadingText:         m.loadingText,
		SpinnerFrame:        m.spinner.View(),
		PosterArtwork:       m.imagesEnabled,
		AccentName:          accentName,
		Transitions:         m.transitions,
		StartupSearchActive: m.startupSearchActive,
		AutoUpdate:          m.autoUpdate,
		DefaultModeName:     defaultModeName,
		ModesRows:           modeRows,
		ProviderRows:        m.providerRows(),
	}
}

func (m *modelImpl) collectDownloadsData(width, height int, accent lipgloss.AdaptiveColor) DownloadsData {
	var active []ActiveDownload

	qualityName := downloadQualityLabel(m.effectiveDownloadQuality())

	if m.batchInProgress && m.batchTotal > 0 {
		seriesTitle := "Batch Download"
		if m.selectedSeries != nil && m.selectedSeries.Title != "" {
			seriesTitle = m.selectedSeries.Title
		}

		currentTitle := fmt.Sprintf("%s (Ep %d/%d)", seriesTitle, m.batchCurrent, m.batchTotal)
		if m.batchCurrent > 0 && m.batchCurrent <= len(m.batchEpisodes) {
			ep := m.batchEpisodes[m.batchCurrent-1]
			if ep.Title != "" {
				currentTitle = fmt.Sprintf("%s — %s (%d/%d)", seriesTitle, ep.Title, m.batchCurrent, m.batchTotal)
			}
		} else if m.batchActiveTitle != "" {
			currentTitle = fmt.Sprintf("%s — %s (%d/%d)", seriesTitle, m.batchActiveTitle, m.batchCurrent, m.batchTotal)
		}

		active = append(active, ActiveDownload{
			Title:      currentTitle,
			Quality:    qualityName,
			Progress:   m.batchEpisodeProgress,
			Speed:      m.downloadSpeed,
			ETA:        m.downloadETA,
			Downloaded: m.downloadDownloaded,
			TotalSize:  m.downloadTotalSize,
			Queued:     false,
			Paused:     m.downloadPaused,
		})

		for i := m.batchCurrent + 1; i <= m.batchTotal; i++ {
			queuedTitle := fmt.Sprintf("%s (Ep %d/%d)", seriesTitle, i, m.batchTotal)
			if i > 0 && i <= len(m.batchEpisodes) {
				ep := m.batchEpisodes[i-1]
				if ep.Title != "" {
					queuedTitle = fmt.Sprintf("%s — %s (%d/%d)", seriesTitle, ep.Title, i, m.batchTotal)
				}
			}
			active = append(active, ActiveDownload{
				Title:   queuedTitle,
				Quality: qualityName,
				Queued:  true,
			})
		}
	} else if (m.cancelDownload != nil || m.downloadOpID != 0 || (m.downloadPaused && m.pendingDownload != nil)) && !m.batchInProgress {
		title := "Download"
		if m.downloadTitle != "" {
			title = m.downloadTitle
		} else if m.pendingDownload != nil {
			title = m.pendingDownload.Episode.Title
			if title == "" {
				title = m.pendingDownload.Series.Title
			}
		} else if m.resolved != nil && m.resolved.SeriesTitle != "" {
			title = m.resolved.DisplayTitle()
		}

		active = append(active, ActiveDownload{
			Title:      title,
			Quality:    qualityName,
			Progress:   m.downloadProgress / 100.0,
			Speed:      m.downloadSpeed,
			ETA:        m.downloadETA,
			Downloaded: m.downloadDownloaded,
			TotalSize:  m.downloadTotalSize,
			Queued:     false,
			Paused:     m.downloadPaused,
		})
	}

	active = append(active, m.activeDownloads...)

	var done []DoneDownload
	seenDone := make(map[string]bool)

	var diskDone []service.CompletedDownload
	if m.downloadService != nil {
		diskDone = m.downloadService.CompletedDownloads()
	}

	diskPathMap := make(map[string]string, len(diskDone))
	for _, cd := range diskDone {
		diskPathMap[cd.Title] = cd.Path
	}

	for _, d := range m.doneDownloads {
		if m.dismissedDoneDownloads[d.Title] {
			continue
		}
		if d.Path == "" {
			if p, ok := diskPathMap[d.Title]; ok {
				d.Path = p
			}
		}
		done = append(done, d)
		seenDone[d.Title] = true
	}

	for _, cd := range diskDone {
		if m.dismissedDoneDownloads[cd.Title] || seenDone[cd.Title] {
			continue
		}
		done = append(done, DoneDownload{
			Title:   cd.Title,
			Quality: "-",
			Size:    cd.Size,
			Path:    cd.Path,
		})
		seenDone[cd.Title] = true
	}

	return DownloadsData{
		Active:        active,
		Done:          done,
		SelectedIndex: m.downloadsIndex,
		Width:         width,
		Height:        height,
		Accent:        accent,
	}
}
