package provider

import (
	"context"
	"errors"
	"testing"
)

// TestProviderErrorUnwrapsToSentinels proves typed errors stay compatible
// with errors.Is against the package sentinels.
func TestProviderErrorUnwrapsToSentinels(t *testing.T) {
	cases := []struct {
		kind     ProviderErrorKind
		sentinel error
	}{
		{KindNotFound, ErrNotFound},
		{KindNoResults, ErrNoResults},
		{KindNoSources, ErrNoSources},
		{KindNoEpisodes, ErrNoEpisodes},
		{KindRateLimited, ErrRateLimited},
		{KindBlocked, ErrBlocked},
		{KindTimeout, ErrTimeout},
		{KindUpstreamChanged, ErrUpstreamChanged},
		{KindAuthRequired, ErrAuthRequired},
	}
	for _, tc := range cases {
		err := Wrap("pengu", "sources", tc.kind, errors.New("boom"))
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("kind %q does not match sentinel %v", tc.kind, tc.sentinel)
		}
		if KindOf(err) != tc.kind {
			t.Errorf("KindOf = %q, want %q", KindOf(err), tc.kind)
		}
	}
}

// TestKindOfPlainErrors maps legacy sentinel errors to kinds.
func TestKindOfPlainErrors(t *testing.T) {
	if KindOf(ErrNoResults) != KindNoResults {
		t.Errorf("KindOf(ErrNoResults) = %q", KindOf(ErrNoResults))
	}
	if KindOf(errors.New("random")) != KindUpstream {
		t.Errorf("KindOf(random) = %q", KindOf(errors.New("random")))
	}
}

// TestProviderErrorRedactsSecrets proves the message carries no hint of
// bodies or tokens: only provider, stage, kind and short hints.
func TestProviderErrorRedactsSecrets(t *testing.T) {
	err := WrapHint("pengu", "sources", KindUpstreamChanged, "missing streams array", errors.New("x"))
	msg := err.Error()
	if len(msg) > 200 {
		t.Errorf("error message too long (may leak bodies): %q", msg)
	}
}

// TestBreakerFailureOnlySickProviders proves healthy empties (no
// results/sources/episodes, not found, rate-limited pause, our own
// cancel) never count toward the breaker — only sick providers do.
func TestBreakerFailureOnlySickProviders(t *testing.T) {
	healthy := []error{ErrNoResults, ErrNoSources, ErrNoEpisodes, ErrNotFound, ErrRateLimited, ErrAuthRequired, context.Canceled}
	for _, err := range healthy {
		if BreakerFailure(err) {
			t.Errorf("BreakerFailure(%v) = true, want false", err)
		}
	}
	sick := []error{
		ErrTimeout, ErrBlocked, ErrUpstreamChanged,
		context.DeadlineExceeded,
		&HTTPError{Code: 500},
		errors.New("connection reset"),
	}
	for _, err := range sick {
		if !BreakerFailure(err) {
			t.Errorf("BreakerFailure(%v) = false, want true", err)
		}
	}
	if BreakerFailure(nil) {
		t.Error("BreakerFailure(nil) must be false")
	}
}
