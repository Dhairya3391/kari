// Package timeouts centralizes every operation budget in Kari.
//
// The values below are derived from measured P95 latencies (see
// tests/live/measure_test.go, run with -tags measure) multiplied by
// SafetyFactor and clamped in Budget. A budget is deliberately generous enough
// that a healthy but slow provider still contributes data, while the Ceiling
// constants guarantee no operation can ever hang the UI.
package timeouts

import "time"

// SafetyFactor pads a measured P95 into a timeout: it absorbs normal jitter
// without inflating the budget into a multi-minute wait. A 7s P95 yields
// 7s * 1.75 = 12.25s.
const SafetyFactor = 1.75

// minBudget is the floor applied by Budget so a trivially fast operation still
// gets a usable deadline.
const minBudget = 500 * time.Millisecond

// Budget returns measured * SafetyFactor clamped to [minBudget, ceiling]. It
// is the single derivation rule for every timeout in the application; callers
// use it in tests and measurement tooling, while the exported constants below
// record the resulting values.
func Budget(measured, ceiling time.Duration) time.Duration {
	if ceiling <= 0 {
		ceiling = time.Duration(float64(measured) * SafetyFactor)
	}
	if ceiling < minBudget {
		ceiling = minBudget
	}
	value := time.Duration(float64(measured) * SafetyFactor)
	if value < minBudget {
		return minBudget
	}
	if value > ceiling {
		return ceiling
	}
	return value
}

// Operation ceilings are absolute backstops independent of measured values.
// No caller may wait longer than these, so the UI can always recover even if a
// provider ignores its context.
const (
	SearchCeiling   = 20 * time.Second
	EpisodeCeiling  = 45 * time.Second
	ResolveCeiling  = 90 * time.Second
	ManifestCeiling = 8 * time.Second
	SubtitleCeiling = 30 * time.Second
	SkipCeiling     = 25 * time.Second
)

// Search budgets. Measured P95 ~7s (user search across all providers); the
// per-provider budget is 7s * 1.75 = 12.25s, the fan-out budget 10s * 1.75 =
// 17.5s (below the 20s ceiling). First-result and grace windows keep the UI
// responsive while backfill collects stragglers.
const (
	SearchPerProvider = 12250 * time.Millisecond
	SearchOverall     = 17500 * time.Millisecond
	SearchFirstResult = 3500 * time.Millisecond // measured 2s * 1.75
	SearchGrace       = 1750 * time.Millisecond // measured 1s * 1.75
	SearchGraceFast   = 875 * time.Millisecond  // measured 500ms * 1.75
	SearchBackfill    = 15 * time.Second        // detached collection after an early return
)

// Source-resolution budgets. Measured P95 ~30s for a multi-lane live resolve
// (52.5s) and ~50s for the full fan-out (87.5s, inside the 90s ceiling).
// Manifest probes measured ~2s, so 4s leaves room for slow CDNs.
const (
	SourcePerProvider = 52500 * time.Millisecond
	SourceOverall     = 87500 * time.Millisecond
	ManifestRequest   = 3500 * time.Millisecond // measured 2s * 1.75
	ManifestBatch     = 4375 * time.Millisecond // measured 2.5s * 1.75
	ManifestQuality   = 4375 * time.Millisecond // measured 2.5s * 1.75
)

// Episode budgets. Titles measured ~2s (3.5s); a full episode load ~25s
// (43.75s, inside the 45s ceiling).
const (
	EpisodesOverall  = 43750 * time.Millisecond
	EpisodeTitles    = 3500 * time.Millisecond
	EpisodeTitleFill = 15 * time.Second // background backfill of real titles
)

// Subtitle budgets. Measured ~12s for a full candidate fetch (21s) and ~6s
// per download (10.5s), inside the 30s overall ceiling.
const (
	SubtitleFetch          = 21 * time.Second
	SubtitleDownload       = 10500 * time.Millisecond
	SubtitleCandidateLimit = 5
)

// Skip-data budgets. Single skip APIs measured ~0.5s but hybrid lookups fan
// out across up to four providers with multi-stage ID resolution, so the
// background budget is measured ~12s * 1.75 = 21s (inside the 25s ceiling).
// Late results are injected into the live player and cached for next time.
const (
	// SkipForegroundWait is a UI responsiveness budget, not a data deadline:
	// late results are injected into the live player and cached. Keeping it
	// short means playback never waits on a slow skip provider.
	SkipForegroundWait = 2 * time.Second
	SkipBackground     = 21 * time.Second
	SkipHTTP           = 14 * time.Second        // measured 8s * 1.75
	SkipIMDbFallback   = 8750 * time.Millisecond // measured 5s * 1.75
)

// Live verification and mirror budgets. A probe measured ~4s (7s), a mirror
// lookup ~6s (10.5s), with a 1s grace (1.75s) for extra mirrors.
const (
	LiveVerify      = 7 * time.Second
	LiveMirror      = 10500 * time.Millisecond
	LiveMirrorGrace = 1750 * time.Millisecond
)

// Shared HTTP client budget. Direct API calls measured 0.5-1.8s; 10s * 1.75
// = 17.5s absorbs slow upstreams without hiding genuine failures.
const (
	HTTPDefault      = 17500 * time.Millisecond
	HTTPDial         = 3500 * time.Millisecond // measured 2s * 1.75
	HTTPDialTLS      = 5250 * time.Millisecond // measured 3s * 1.75
	HTTPTLSHandshake = 17500 * time.Millisecond
)

// Provider-internal fan-out budgets, each measured P95 * 1.75.
const (
	BingrMeta          = 2625 * time.Millisecond  // measured 1.5s * 1.75
	BingrShard         = 5250 * time.Millisecond  // measured 3s * 1.75
	BingrFanout        = 8750 * time.Millisecond  // measured 5s * 1.75
	PenguSubtitleCtx   = 3500 * time.Millisecond  // measured 2s * 1.75
	CinejoyServer      = 5250 * time.Millisecond  // measured 3s * 1.75
	CinejoyLane        = 10500 * time.Millisecond // measured 6s * 1.75
	SweetfoxLane       = 14 * time.Second         // measured 8s * 1.75
	SweetfoxMaster     = 7 * time.Second          // measured 4s * 1.75
	SolisCheck         = 3500 * time.Millisecond  // measured 2s * 1.75
	StreambaseFallback = 10500 * time.Millisecond // measured 6s * 1.75
	LaneBudget         = 10500 * time.Millisecond // measured 6s * 1.75

	// Quorum-based provider schedulers use integer millisecond deadlines.
	// Each value is the measured P95 * 1.75; the request cap always stays
	// above the soft deadline so a lane is never cut off mid-collection.
	MovysxRequestMS   = 25000
	MovysxSoftMS      = 10500
	MovysxGraceMS     = 3500
	MovysxMirrorProbe = 3500 * time.Millisecond
	MovysxMirrorTotal = 5250 * time.Millisecond
)
