package service

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"kari/internal/lang"
	"kari/internal/model"
	"kari/internal/provider"
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
	tierCounts  map[string]int
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
func mergeDuplicateSource(dst *provider.MediaSource, src provider.MediaSource) {
	if dst.SubType == "" {
		dst.SubType = src.SubType
	}
	if dst.Backend == "" {
		dst.Backend = src.Backend
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
	if len(dst.ExtraArgs) == 0 {
		dst.ExtraArgs = src.ExtraArgs
	}
	if SourceQuality(src.Quality) > SourceQuality(dst.Quality) {
		dst.Quality = src.Quality
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
// precedence, ensuring primary/faster native providers (like anicine) win over fallbacks.
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

// tierKey groups interchangeable mirror rows: same resolver, same
// quality height, same backend, same audio language.
func tierKey(src provider.MediaSource) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(src.Resolver)),
		strings.TrimSpace(strings.ToLower(src.Language)),
		strings.TrimSpace(src.Backend),
		strconv.Itoa(SourceQuality(src.Quality)),
	}, "\x00")
}

// tierHasRoom reports whether another row of src's tier fits, counting
// it when it does. Callers hold the aggregation lock.
func (a *sourceAggregator) tierHasRoom(src provider.MediaSource) bool {
	if a.tierCounts == nil {
		a.tierCounts = make(map[string]int)
	}
	key := tierKey(src)
	if a.tierCounts[key] >= maxSourcesPerTier {
		return false
	}
	a.tierCounts[key]++
	return true
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

// maxSourcesPerTier caps rows sharing one (resolver, quality height,
// backend, language) identity: upstream mirrors multiply identical
// rows (16 bare "1080p" from one resolver), and the first arrivals are
// already best-first, so the cap cuts noise while keeping fallback
// depth. Distinct backends, heights and dubbed languages each keep
// their own budget and are never collapsed into each other.
const maxSourcesPerTier = 3

// add merges one provider batch. Independently re-signed copies of the same
// CDN media collapse into one row, while the kept row's full transport
// identity still owns its subtitles so playback headers and source matching
// stay exact.
func (a *sourceAggregator) add(providerName string, batch []provider.MediaSource) {
	if a.sourceIndex == nil {
		a.sourceIndex = make(map[string]int)
	}
	slices.SortStableFunc(batch, func(x, y provider.MediaSource) int {
		xh := strings.EqualFold(strings.TrimSpace(x.SubType), provider.SubTypeHard) && !provider.ChallengedHost(x.URL)
		yh := strings.EqualFold(strings.TrimSpace(y.SubType), provider.SubTypeHard) && !provider.ChallengedHost(y.URL)
		if xh == yh {
			return 0
		}
		if xh {
			return -1
		}
		return 1
	})
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
				a.retargetSubtitles(oldIdentity, kept.TransportIdentity(), kept.URL, src.Resolver)
			} else {
				old := a.sources[index]
				oldIdentity := old.TransportIdentity()
				mergeDuplicateSource(&a.sources[index], src)
				kept = a.sources[index]
				if oldIdentity != kept.TransportIdentity() {
					a.retargetSubtitles(oldIdentity, kept.TransportIdentity(), kept.URL, kept.Resolver)
				}
			}
		} else if a.tierHasRoom(src) {
			a.sourceIndex[dedupeKey] = len(a.sources)
			a.sources = append(a.sources, src)
		} else {
			continue
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
