package service

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"kari/internal/lang"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/provider/kit"
)

// sourceAggregator merges playback sources and subtitle tracks from all
// providers, deduplicating entries as they arrive. It is not safe for
// concurrent use; callers must hold their own lock around add/sort/publish
// sequencing (see Resolve).
type sourceAggregator struct {
	priority    map[string]int // provider name -> priority (lower sorts first)
	displayName func(string) string
	sources     []provider.MediaSource
	sourceIndex map[string]int // canonical source identity -> index in sources
	subs        []model.SubtitleTrack
	seenSubs    map[string]struct{}
}

// sourceDedupeIdentity is the identity of the media address and its required
// transport headers, with volatile signing parameters removed. User-Agent is
// deliberately excluded: it is a client hint, not part of the address, and
// providers commonly differ on whether they set it for the same CDN URL. The
// full TransportIdentity still owns playback and subtitle matching.
func sourceDedupeIdentity(src provider.MediaSource) string {
	rawURL := provider.CanonicalSourceURL(src.URL)
	if rawURL == "" {
		return ""
	}
	return strings.Join([]string{
		rawURL,
		strings.TrimSpace(src.Referer),
		strings.TrimSpace(src.CookieHeader),
		strings.TrimSpace(src.Language),
		strconv.FormatBool(src.SuppressOrigin),
		strings.Join(src.ExtraArgs, "\x00"),
	}, "\x00")
}

// mergeDuplicateSource folds descriptive metadata from a re-signed copy
// into the row already kept. The kept URL and headers stay untouched so the
// playable transport remains exactly what its provider resolved.
func mergeDuplicateSource(dst *provider.MediaSource, src provider.MediaSource) bool {
	before := *dst
	if dst.SubType == "" {
		dst.SubType = src.SubType
	}
	if dst.Backend == "" {
		dst.Backend = src.Backend
	}
	if dst.ManifestURL == "" {
		dst.ManifestURL = src.ManifestURL
	}
	if dst.PreferredAudio == "" {
		dst.PreferredAudio = src.PreferredAudio
	}
	dst.FallbackOnly = dst.FallbackOnly && src.FallbackOnly
	if dst.Referer == "" {
		dst.Referer = src.Referer
	}
	if dst.UserAgent == "" {
		dst.UserAgent = src.UserAgent
	}
	if dst.CookieHeader == "" {
		dst.CookieHeader = src.CookieHeader
	}
	if dst.Language == "" {
		dst.Language = src.Language
	}
	if dst.AudioLabel == "" {
		dst.AudioLabel = src.AudioLabel
	}
	if len(dst.ExtraArgs) == 0 {
		dst.ExtraArgs = slices.Clone(src.ExtraArgs)
	}
	if !kit.IsAdaptiveQuality(src.Quality) &&
		(kit.IsAdaptiveQuality(dst.Quality) || kit.QualityHeight(src.Quality) > kit.QualityHeight(dst.Quality)) {
		dst.Quality = src.Quality
	}
	if src.Height > dst.Height {
		dst.Height = src.Height
	}
	if src.Bitrate > dst.Bitrate {
		dst.Bitrate = src.Bitrate
	}
	if len(dst.Audio.Languages) == 0 {
		dst.Audio.Languages = src.Audio.Languages
	}
	if dst.Audio.Track == provider.TrackUnknown {
		dst.Audio.Track = src.Audio.Track
	}
	var missing []provider.SubtitleOption
	for _, sub := range src.Subtitles {
		if !hasSubtitleOption(dst.Subtitles, sub) && !hasSubtitleOption(missing, sub) {
			missing = append(missing, sub)
		}
	}
	if len(missing) > 0 {
		merged := make([]provider.SubtitleOption, 0, len(dst.Subtitles)+len(missing))
		merged = append(merged, dst.Subtitles...)
		merged = append(merged, missing...)
		dst.Subtitles = merged
	}
	return before.SubType != dst.SubType || before.Backend != dst.Backend ||
		before.FallbackOnly != dst.FallbackOnly || before.Referer != dst.Referer ||
		before.UserAgent != dst.UserAgent || before.CookieHeader != dst.CookieHeader ||
		before.ManifestURL != dst.ManifestURL ||
		before.PreferredAudio != dst.PreferredAudio ||
		before.Language != dst.Language || before.AudioLabel != dst.AudioLabel ||
		!slices.Equal(before.Audio.Languages, dst.Audio.Languages) ||
		before.Quality != dst.Quality || before.Bitrate != dst.Bitrate ||
		!slices.Equal(before.ExtraArgs, dst.ExtraArgs) ||
		len(before.Subtitles) != len(dst.Subtitles)
}

// cloneMediaSources detaches provider-owned data and keeps published snapshots
// independent even when consumers change nested playback options.
func cloneMediaSources(sources []provider.MediaSource) []provider.MediaSource {
	cloned := slices.Clone(sources)
	for i := range cloned {
		cloned[i].Subtitles = slices.Clone(cloned[i].Subtitles)
		cloned[i].ExtraArgs = slices.Clone(cloned[i].ExtraArgs)
		if cloned[i].SkipTimes != nil {
			skipTimes := *cloned[i].SkipTimes
			cloned[i].SkipTimes = &skipTimes
		}
	}
	return cloned
}

// hasSubtitleOption reports whether options already carries the same
// caption track (URL plus language).
func hasSubtitleOption(options []provider.SubtitleOption, candidate provider.SubtitleOption) bool {
	candidateURL := strings.TrimSpace(candidate.URL)
	candidateLang := strings.ToLower(strings.TrimSpace(candidate.Language))
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.URL), candidateURL) &&
			strings.ToLower(strings.TrimSpace(option.Language)) == candidateLang {
			return true
		}
	}
	return false
}

// shouldPreferSource reports whether candidate should replace existing as the
// canonical row for duplicate streams. Lower provider priority index takes
// precedence, ensuring primary/faster native providers win over fallbacks.
func (a *sourceAggregator) shouldPreferSource(existing, candidate provider.MediaSource) bool {
	pCand, hasCand := a.priority[candidate.Resolver]
	pExist, hasExist := a.priority[existing.Resolver]
	if hasCand && hasExist && pCand < pExist {
		return true
	}
	return false
}

// retargetSubtitles updates existing subtitles whose source was replaced by
// a preferred duplicate provider, keeping their SourceID and SourceURL valid.
func (a *sourceAggregator) retargetSubtitles(oldID, newID, newURL, newResolver string) {
	if oldID == "" || newID == "" {
		return
	}
	for i := range a.subs {
		if a.subs[i].SourceID == oldID {
			if oldID != newID {
				oldSubURL := a.subs[i].URL
				delete(a.seenSubs, oldSubURL+"\x00"+oldID)
				a.seenSubs[oldSubURL+"\x00"+newID] = struct{}{}
				a.subs[i].SourceID = newID
				a.subs[i].SourceURL = newURL
			}
			if newResolver != "" {
				a.subs[i].Resolver = newResolver
				a.subs[i].ProviderName = a.displayName(newResolver)
				a.subs[i].Label = fmt.Sprintf("%s (%s)", lang.Name(a.subs[i].Language), a.subs[i].ProviderName)
			}
		}
	}
}

// newSourceAggregator builds an aggregator that preserves provider priority.
func newSourceAggregator(providers []provider.Provider, displayName func(string) string) *sourceAggregator {
	priority := make(map[string]int, len(providers))
	for i, p := range providers {
		priority[p.Name()] = i
	}
	return &sourceAggregator{
		priority:    priority,
		displayName: displayName,
		sourceIndex: make(map[string]int),
		seenSubs:    make(map[string]struct{}),
	}
}

// add merges one provider batch. Independently re-signed copies of the same
// CDN media collapse into one row, while the kept row's full transport
// identity still owns its subtitles so playback headers and source matching
// stay exact. It reports whether any published source or subtitle changed.
func (a *sourceAggregator) add(providerName string, batch []provider.MediaSource) bool {
	if len(batch) == 0 {
		return false
	}
	changed := false
	batch = cloneMediaSources(batch)
	if a.sourceIndex == nil {
		a.sourceIndex = make(map[string]int)
	}

	for _, src := range batch {
		src.Resolver = providerName
		identity := src.TransportIdentity()
		if identity == "" {
			continue
		}
		dedupeKey := sourceDedupeIdentity(src)
		kept := src
		if index, ok := a.sourceIndex[dedupeKey]; ok {
			if a.shouldPreferSource(a.sources[index], src) {
				old := a.sources[index]
				oldIdentity := old.TransportIdentity()
				kept = src
				mergeDuplicateSource(&kept, old)
				a.sources[index] = kept
				changed = true
				a.retargetSubtitles(oldIdentity, kept.TransportIdentity(), kept.URL, src.Resolver)
			} else {
				old := a.sources[index]
				oldIdentity := old.TransportIdentity()
				if mergeDuplicateSource(&a.sources[index], src) {
					changed = true
				}
				kept = a.sources[index]
				if oldIdentity != kept.TransportIdentity() {
					a.retargetSubtitles(oldIdentity, kept.TransportIdentity(), kept.URL, kept.Resolver)
				}
			}
		} else {
			a.sourceIndex[dedupeKey] = len(a.sources)
			a.sources = append(a.sources, src)
			changed = true
		}
		keptIdentity := kept.TransportIdentity()
		for _, sub := range src.Subtitles {
			subURL := strings.TrimSpace(sub.URL)
			if subURL == "" {
				continue
			}
			key := subURL + "\x00" + keptIdentity
			if _, seen := a.seenSubs[key]; seen {
				continue
			}
			a.seenSubs[key] = struct{}{}
			changed = true
			subLang := lang.Normalize(sub.Language)
			referer := strings.TrimSpace(sub.Referer)
			if referer == "" {
				referer = src.Referer
			}
			a.subs = append(a.subs, model.SubtitleTrack{
				Label:        fmt.Sprintf("%s (%s)", lang.Name(subLang), a.displayName(kept.Resolver)),
				Language:     subLang,
				URL:          subURL,
				Referer:      referer,
				SourceURL:    kept.URL,
				SourceID:     keptIdentity,
				Default:      sub.Default,
				Resolver:     kept.Resolver,
				ProviderName: a.displayName(kept.Resolver),
			})
		}
	}
	return changed
}

// replaceProviderSources swaps a provider's published rows for batch. Late
// enrichment can reveal that rows added from earlier progressive snapshots
// were shadowed duplicates; add() cannot remove them, so callers use this to
// replace the provider's whole contribution before the final publish.
func (a *sourceAggregator) replaceProviderSources(providerName string, batch []provider.MediaSource) bool {
	// An empty batch means "no update", not "remove everything": earlier
	// snapshots may have published valid rows that add() could not replace.
	valid := make([]provider.MediaSource, 0, len(batch))
	for _, src := range batch {
		if sourceDedupeIdentity(src) != "" {
			valid = append(valid, src)
		}
	}
	if len(valid) == 0 {
		return false
	}

	kept := make([]provider.MediaSource, 0, len(a.sources))
	for _, src := range a.sources {
		if !strings.EqualFold(src.Resolver, providerName) {
			kept = append(kept, src)
		}
	}
	a.sources = kept
	a.sourceIndex = make(map[string]int, len(a.sources)+len(valid))
	for i, src := range a.sources {
		a.sourceIndex[sourceDedupeIdentity(src)] = i
	}

	// Subtitle tracks belong to the source row they were fetched with. Drop
	// tracks whose row is gone, then let add() re-register the new batch's
	// tracks through the normal dedupe path.
	live := make(map[string]struct{}, len(a.sources))
	for _, src := range a.sources {
		live[src.TransportIdentity()] = struct{}{}
	}
	subs := a.subs[:0]
	for _, sub := range a.subs {
		if _, ok := live[sub.SourceID]; ok {
			subs = append(subs, sub)
		}
	}
	a.subs = subs
	a.seenSubs = make(map[string]struct{}, len(a.subs))
	for _, sub := range a.subs {
		a.seenSubs[strings.TrimSpace(sub.URL)+"\x00"+sub.SourceID] = struct{}{}
	}
	return a.add(providerName, valid)
}

// sort preserves the arrival order of playable sources across providers,
// pushing only anti-bot challenged hosts to the bottom. It reindexes the
// dedupe map because sorting reorders the backing slice.
func (a *sourceAggregator) sort() {
	slices.SortStableFunc(a.sources, func(si, sj provider.MediaSource) int {
		return challengedRank(si) - challengedRank(sj)
	})
	a.reindexSources()
}

// reindexSources rebuilds the dedupe map after sorting. add and snapshot both
// run under the service lock, but sort reorders the slice, so stale indices
// would merge metadata into the wrong source on the next provider batch.
func (a *sourceAggregator) reindexSources() {
	if a.sourceIndex == nil {
		a.sourceIndex = make(map[string]int)
	}
	clear(a.sourceIndex)
	for i := range a.sources {
		a.sourceIndex[sourceDedupeIdentity(a.sources[i])] = i
	}
}

// challengedRank orders challenged-host sources below every playable-now
// source, mirroring the ranking package so the aggregate, Preview, and
// playback agree on what plays first.
func challengedRank(s provider.MediaSource) int {
	if provider.ChallengedHost(s.URL) {
		return 1
	}
	return 0
}

func firstPlaybackURL(playback []provider.MediaSource) string {
	if len(playback) == 0 {
		return ""
	}
	return playback[0].URL
}
