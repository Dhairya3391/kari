package ranking

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"kari/internal/provider"
)

// Quality mode constants matching Kari's settings.
const (
	QualityAll       = 0
	QualityHighest   = 1
	QualityDataSaver = 2
	QualityLowest    = 3
)

// Criteria encapsulates user preferences and contextual signals used to score and rank media sources.
type Criteria struct {
	Mode                  provider.ContentType
	QualityMode           int
	EnabledAudioLanguages []string
	AnimeAudio            string // "sub" or "dub"
	AnimeSubtitlesEnabled bool
	PreferredSubtitleLang string
	StickyProvider        string
	FailedProviders       map[string]time.Time
	Now                   time.Time
}

// ScoredSource wraps a MediaSource with its calculated ranking score and original index.
type ScoredSource struct {
	Source         provider.MediaSource
	OriginalIndex  int
	TransportScore int
	AudioScore     int
	SubtitleScore  int
	QualityScore   float64
	SubTypeScore   int
	StickyScore    int
	HealthScore    int
}

var (
	reBracketTag = regexp.MustCompile(`\[[^\]]+\]`)
	reQuality4K  = regexp.MustCompile(`(?i)\b(4k|uhd|2160p?)\b`)
	reQualityQHD = regexp.MustCompile(`(?i)\b(qhd|1440p?|2k)\b`)
	reQualityFHD = regexp.MustCompile(`(?i)\b(fhd|1080p?)\b`)
	reQualityHD  = regexp.MustCompile(`(?i)\b(hd|720p?)\b`)
	reQuality576 = regexp.MustCompile(`(?i)\b(576p?)\b`)
	reQualitySD  = regexp.MustCompile(`(?i)\b(sd|480p?)\b`)
	reQuality360 = regexp.MustCompile(`(?i)\b(360p?)\b`)
	reQualityP   = regexp.MustCompile(`(?i)(\d{3,4})p`)
	reQualityNum = regexp.MustCompile(`\b(\d{3,4})\b`)
	reFileSize   = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(gb|mb|gib|mib|kb)\b`)
)

func ParseResolution(raw string) int {
	stripped := reBracketTag.ReplaceAllString(raw, "")
	lower := strings.ToLower(strings.TrimSpace(stripped))
	if lower == "" || lower == "unknown" || lower == "—" {
		return 0
	}
	switch lower {
	case "1080p", "1080", "fhd":
		return 1080
	case "720p", "720", "hd":
		return 720
	case "2160p", "2160", "4k", "uhd":
		return 2160
	case "1440p", "1440", "qhd", "2k":
		return 1440
	case "480p", "480", "sd":
		return 480
	case "360p", "360":
		return 360
	case "576p", "576":
		return 576
	}
	if strings.Contains(lower, "auto") || strings.Contains(lower, "master") {
		return 1080
	}

	if reQuality4K.MatchString(lower) {
		return 2160
	}
	if reQualityQHD.MatchString(lower) {
		return 1440
	}
	if reQualityFHD.MatchString(lower) {
		return 1080
	}
	if reQualityHD.MatchString(lower) {
		return 720
	}
	if reQuality576.MatchString(lower) {
		return 576
	}
	if reQualitySD.MatchString(lower) {
		return 480
	}
	if reQuality360.MatchString(lower) {
		return 360
	}

	if m := reQualityP.FindStringSubmatch(lower); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n
		}
	}
	if m := reQualityNum.FindStringSubmatch(lower); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n
		}
	}

	return 0
}

// ParseFileSizeMB extracts file size in megabytes if present in the string.
func ParseFileSizeMB(raw string) float64 {
	m := reFileSize.FindStringSubmatch(raw)
	if len(m) < 3 {
		return 0
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(m[2])
	switch unit {
	case "gb", "gib":
		return val * 1024
	case "mb", "mib":
		return val
	case "kb":
		return val / 1024
	default:
		return 0
	}
}

// fullHDThreshold is the minimum parsed resolution kept visible under the
// Highest quality mode: FHD (1080p) and anything above it (QHD, 4K).
// Adaptive "Auto" masters parse as 1080 via ParseResolution, so they stay.
const fullHDThreshold = 1080

// KeepHighestVisible narrows sources to what the Highest quality mode may
// show in Preview: every row at FHD (1080p) or better. When no row reaches
// FHD it keeps every row at the highest available tier instead, so a
// low-only list still shows its best rather than an empty table. Rows with
// unparseable labels are kept only when nothing parses at all.
func KeepHighestVisible(sources []provider.MediaSource) []provider.MediaSource {
	if len(sources) == 0 {
		return nil
	}
	res := make([]int, len(sources))
	best := 0
	for i, src := range sources {
		r := ParseResolution(src.Quality)
		res[i] = r
		if r > best {
			best = r
		}
	}
	if best == 0 {
		return sources
	}
	threshold := fullHDThreshold
	if best < fullHDThreshold {
		threshold = best
	}
	kept := make([]provider.MediaSource, 0, len(sources))
	for i, src := range sources {
		if res[i] >= threshold {
			kept = append(kept, src)
		}
	}
	return kept
}

// RankSources sorts playback sources according to the criteria defined in KARI_TUI_SPEC §5.
// Returns a reordered slice of ScoredSource where index 0 is the "best" source.
func RankSources(sources []provider.MediaSource, crit Criteria) []ScoredSource {
	if len(sources) == 0 {
		return nil
	}

	now := crit.Now
	if now.IsZero() {
		now = time.Now()
	}

	scored := make([]ScoredSource, len(sources))
	for i, src := range sources {
		scored[i] = ScoredSource{
			Source:         src,
			OriginalIndex:  i,
			TransportScore: scoreTransport(src),
			AudioScore:     scoreAudio(src, crit),
			SubtitleScore:  scoreSubtitles(src, crit),
			QualityScore:   scoreQuality(src, crit.QualityMode),
			SubTypeScore:   scoreSubType(src, crit.Mode),
			StickyScore:    scoreSticky(src, crit.StickyProvider),
			HealthScore:    scoreHealth(src, crit.FailedProviders, now),
		}
	}

	// Sort stably using the hierarchy from spec §5 plus the subtitle-kind
	// stage: hardsubs read without any subtitle plumbing, so they sort
	// ahead of softsubs at equal quality; unknown stays neutral.
	// 0. Transport (challenged hosts last, always)
	// 1. Health
	// 2. Audio match
	// 3. Subtitle availability
	// 4. Quality
	// 5. Subtitle kind (hardsub first)
	// 6. Sticky provider
	// 7. Original provider order (stable tiebreak)
	slices.SortFunc(scored, func(a, b ScoredSource) int {
		return compareScored(b, a)
	})

	return scored
}

func compareScored(a, b ScoredSource) int {
	// Challenged transport always loses, before every other signal.
	if a.TransportScore != b.TransportScore {
		return a.TransportScore - b.TransportScore
	}
	// Health demotion
	if a.HealthScore != b.HealthScore {
		return a.HealthScore - b.HealthScore
	}

	// 1. Audio match
	if a.AudioScore != b.AudioScore {
		return a.AudioScore - b.AudioScore
	}

	// 2. Subtitle availability
	if a.SubtitleScore != b.SubtitleScore {
		return a.SubtitleScore - b.SubtitleScore
	}

	// 3. Quality score (2160 > 1080 > 720 > 480 > 360 for Highest mode)
	if a.QualityScore != b.QualityScore {
		if a.QualityScore > b.QualityScore {
			return 1
		}
		return -1
	}

	// 4. Subtitle kind (hardsub first)
	if a.SubTypeScore != b.SubTypeScore {
		return a.SubTypeScore - b.SubTypeScore
	}

	// 5. Sticky provider
	if a.StickyScore != b.StickyScore {
		return a.StickyScore - b.StickyScore
	}

	// 6. Stable tiebreak: earlier index in original slice wins
	return b.OriginalIndex - a.OriginalIndex
}

func scoreTransport(src provider.MediaSource) int {
	if provider.ChallengedHost(src.URL) {
		return -1
	}
	return 0
}

func scoreAudio(src provider.MediaSource, crit Criteria) int {
	rawLang := strings.TrimSpace(src.Language)

	if crit.Mode == provider.ModeAnime {
		pref := strings.ToLower(strings.TrimSpace(crit.AnimeAudio))
		if pref == "" {
			pref = provider.AudioSub
		}
		target := strings.ToLower(rawLang + " " + src.Quality + " " + src.Resolver)
		if strings.Contains(target, pref) {
			return 2
		}
		// If explicitly opposite track, demote
		opposite := provider.AudioDub
		if pref == provider.AudioDub {
			opposite = provider.AudioSub
		}
		if strings.Contains(target, opposite) {
			return -2
		}
		return 0 // Neutral when unspecified
	}

	// Movies / TV: check order in EnabledAudioLanguages
	if rawLang == "" {
		return 0 // Neutral when unspecified
	}

	if len(crit.EnabledAudioLanguages) == 0 {
		return 0
	}

	haystack := strings.ToLower(rawLang + " " + src.Quality)
	total := len(crit.EnabledAudioLanguages)
	for idx, lang := range crit.EnabledAudioLanguages {
		l := strings.ToLower(strings.TrimSpace(lang))
		if l != "" && strings.Contains(haystack, l) {
			return total - idx // Earlier in list yields higher positive score
		}
	}
	return 0
}

func scoreSubtitles(src provider.MediaSource, crit Criteria) int {
	pref := strings.ToLower(strings.TrimSpace(crit.PreferredSubtitleLang))
	if pref == "" {
		pref = "english"
	}

	if crit.Mode == provider.ModeAnime && !crit.AnimeSubtitlesEnabled {
		return 0
	}

	for _, sub := range src.Subtitles {
		lang := strings.ToLower(strings.TrimSpace(sub.Language))
		if lang == pref || strings.HasPrefix(lang, pref[:min(len(pref), 2)]) {
			return 1
		}
	}
	return 0
}

// scoreSubType ranks declared subtitle kinds: hardsubs need no subtitle
// plumbing so they sort first, softsubs last, unknown stays neutral.
func scoreSubType(src provider.MediaSource, mode provider.ContentType) int {
	if mode != provider.ModeAnime {
		return 1
	}
	if len(src.Subtitles) > 0 {
		return 0
	}
	switch strings.TrimSpace(src.SubType) {
	case provider.SubTypeHard:
		return 2
	case provider.SubTypeSoft:
		return 0
	default:
		return 1
	}
}

func scoreQuality(src provider.MediaSource, mode int) float64 {
	res := float64(ParseResolution(src.Quality))
	sizeMB := ParseFileSizeMB(src.Quality)

	switch mode {
	case QualityLowest:
		if res > 0 {
			return 10000.0 - res
		}
		return 0.0

	case QualityDataSaver:
		// Prefer <= 1080p, then highest resolution among them, then smaller file size
		if res <= 1080 && res > 0 {
			score := 20000.0 + res
			if sizeMB > 0 {
				score += math.Max(0, 5000.0-sizeMB) / 100.0
			}
			return score
		}
		// Higher than 1080p gets lower priority in Data Saver
		return res

	case QualityHighest, QualityAll:
		fallthrough
	default:
		// Highest resolution first (2160 > 1080 > 720 > 480 > 360)
		score := res * 100.0
		if sizeMB > 0 {
			score += sizeMB / 1000.0
		}
		return score
	}
}

func scoreSticky(src provider.MediaSource, sticky string) int {
	if sticky == "" {
		return 0
	}
	if strings.EqualFold(src.Resolver, sticky) {
		return 1
	}
	return 0
}

func scoreHealth(src provider.MediaSource, failed map[string]time.Time, now time.Time) int {
	if len(failed) == 0 {
		return 0
	}
	resolver := strings.ToLower(strings.TrimSpace(src.Resolver))
	if lastFail, ok := failed[resolver]; ok {
		if now.Sub(lastFail) < 10*time.Minute {
			return -100 // Demote unhealthy provider
		}
	}
	return 0
}
