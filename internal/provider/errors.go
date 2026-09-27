package provider

import (
	"context"
	"errors"
	"fmt"
)

// Sentinel errors used by providers for common failure cases.
var (
	ErrNoResults    = errors.New("no results found")
	ErrNoEpisodes   = errors.New("no episodes found")
	ErrNoSources    = errors.New("no sources found")
	ErrNotFound     = errors.New("not found")
	ErrAuthRequired = errors.New("authentication required")
	ErrRateLimited  = errors.New("rate limited")
	// ErrBlocked reports a 403/challenge block from the upstream.
	ErrBlocked = errors.New("upstream blocked the request")
	// ErrTimeout reports a provider deadline without conflating it
	// with an empty catalog.
	ErrTimeout = errors.New("provider timed out")
	// ErrUpstreamChanged reports that the expected upstream structure
	// is missing (HTML/JSON shape drift). It must never surface as
	// ErrNoResults/ErrNoSources so catalog drift is distinguishable
	// from a genuinely empty catalog.
	ErrUpstreamChanged = errors.New("upstream response changed")
	// ErrNoMatch reports that cross-provider title resolution found no
	// candidate above the acceptance threshold.
	ErrNoMatch = errors.New("no cross-provider match")
	// ErrAudioUnavailable reports that an audio track verifiably does not
	// exist for an episode (e.g. no dub servers listed, embed page has no
	// file). Unlike transport errors it is stable: retrying cannot help,
	// and episode lists may hide the entry.
	ErrAudioUnavailable = errors.New("audio track unavailable")
)

// HTTPError represents a non-2xx HTTP response from an upstream API.
type HTTPError struct {
	Code int
	URL  string
}

// Error renders the status code and URL.
func (e *HTTPError) Error() string {
	if e == nil {
		return "http error"
	}
	if e.URL == "" {
		return fmt.Sprintf("http status %d", e.Code)
	}
	return fmt.Sprintf("http status %d for %s", e.Code, e.URL)
}

// StatusCode implements StatusCodedError.
func (e *HTTPError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.Code
}

var _ StatusCodedError = (*HTTPError)(nil)

// ProviderErrorKind classifies a provider failure so callers can react
// without string matching. The zero value is KindUpstream, a generic
// upstream failure.
type ProviderErrorKind string

// Provider error kinds.
const (
	KindNotFound         ProviderErrorKind = "not_found"
	KindNoResults        ProviderErrorKind = "no_results"
	KindNoSources        ProviderErrorKind = "no_sources"
	KindNoEpisodes       ProviderErrorKind = "no_episodes"
	KindRateLimited      ProviderErrorKind = "rate_limited"
	KindBlocked          ProviderErrorKind = "blocked"
	KindTimeout          ProviderErrorKind = "timeout"
	KindUpstreamChanged  ProviderErrorKind = "upstream_changed"
	KindAuthRequired     ProviderErrorKind = "auth_required"
	KindAudioUnavailable ProviderErrorKind = "audio_unavailable"
	KindUpstream         ProviderErrorKind = "upstream"
)

// ProviderError is a typed provider failure: which provider, which stage
// (search/items/sources), what kind, and the underlying cause. Hint is a
// short redacted structural note (e.g. "missing .results array"), never a
// response body, token, or URL with credentials.
type ProviderError struct {
	Provider string
	Stage    string
	Kind     ProviderErrorKind
	Hint     string
	Err      error
}

// Error renders the provider failure without leaking secrets.
func (e *ProviderError) Error() string {
	if e == nil {
		return "provider error"
	}
	if e.Hint != "" {
		return fmt.Sprintf("%s %s: %s (%s)", e.Provider, e.Stage, e.Kind, e.Hint)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s %s: %s: %v", e.Provider, e.Stage, e.Kind, e.Err)
	}
	return fmt.Sprintf("%s %s: %s", e.Provider, e.Stage, e.Kind)
}

// Unwrap exposes both the sentinel matching Kind and the underlying
// cause so errors.Is works against the package sentinels as well as
// wrapped causes.
func (e *ProviderError) Unwrap() []error {
	if e == nil {
		return nil
	}
	var out []error
	switch e.Kind {
	case KindNotFound:
		out = append(out, ErrNotFound)
	case KindNoResults:
		out = append(out, ErrNoResults)
	case KindNoSources:
		out = append(out, ErrNoSources)
	case KindNoEpisodes:
		out = append(out, ErrNoEpisodes)
	case KindRateLimited:
		out = append(out, ErrRateLimited)
	case KindBlocked:
		out = append(out, ErrBlocked)
	case KindTimeout:
		out = append(out, ErrTimeout)
	case KindUpstreamChanged:
		out = append(out, ErrUpstreamChanged)
	case KindAuthRequired:
		out = append(out, ErrAuthRequired)
	}
	if e.Err != nil {
		out = append(out, e.Err)
	}
	return out
}

var _ error = (*ProviderError)(nil)

// Wrap classifies err from provider/stage with kind. A nil err yields nil.
// Hint must be short and redacted (structure notes only).
func Wrap(provider, stage string, kind ProviderErrorKind, err error) error {
	if err == nil {
		return nil
	}
	return &ProviderError{Provider: provider, Stage: stage, Kind: kind, Err: err}
}

// WrapHint classifies err with a redacted structural hint.
func WrapHint(provider, stage string, kind ProviderErrorKind, hint string, err error) error {
	return &ProviderError{Provider: provider, Stage: stage, Kind: kind, Hint: hint, Err: err}
}

// BreakerFailure reports whether err means the provider itself is sick
// (transport/timeout/5xx/challenge) as opposed to a healthy answer:
// no results, no sources, no episodes, not found, rate-limited pause,
// or our own cancel cutting a straggler short. Only sick providers
// count toward the circuit breaker — an empty catalog must never hide
// a provider from later searches.
func BreakerFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	switch KindOf(err) {
	case KindNotFound, KindNoResults, KindNoSources, KindNoEpisodes,
		KindRateLimited, KindAuthRequired, KindAudioUnavailable:
		return false
	}
	return true
}

// KindOf extracts the ProviderErrorKind from err, or KindUpstream when the
// error carries no typed kind.
func KindOf(err error) ProviderErrorKind {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Kind
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return KindNotFound
	case errors.Is(err, ErrNoResults):
		return KindNoResults
	case errors.Is(err, ErrNoSources):
		return KindNoSources
	case errors.Is(err, ErrNoEpisodes):
		return KindNoEpisodes
	case errors.Is(err, ErrRateLimited):
		return KindRateLimited
	case errors.Is(err, ErrBlocked):
		return KindBlocked
	case errors.Is(err, ErrTimeout):
		return KindTimeout
	case errors.Is(err, ErrUpstreamChanged):
		return KindUpstreamChanged
	case errors.Is(err, ErrAuthRequired):
		return KindAuthRequired
	case errors.Is(err, ErrAudioUnavailable):
		return KindAudioUnavailable
	}
	return KindUpstream
}
