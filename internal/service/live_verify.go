package service

import (
	"context"
	"sync"
	"time"

	"kari/internal/provider"
)

const (
	// liveVerifyTimeout bounds the whole verification pass for one provider
	// batch. Probes run concurrently, so this is the added playback-start
	// latency even when several mirrors are dead.
	liveVerifyTimeout = 6 * time.Second
	// liveVerifyParallelism keeps a provider batch from opening dozens of
	// sockets at once while still checking mirrors in parallel.
	liveVerifyParallelism = 8
)

// verifyLiveSources probes sources through the provider's declared verifier
// and keeps the reachable rows in their original order. When nothing verifies,
// the original batch is returned unchanged: a probe can fail for transient or
// geo reasons, and showing unverified rows is better than hiding playback
// entirely. Providers without the capability pass through untouched.
func verifyLiveSources(ctx context.Context, mode provider.ContentType, verifier provider.LiveStreamVerifier, sources []provider.MediaSource) []provider.MediaSource {
	if len(sources) == 0 {
		return sources
	}
	ctx, cancel := context.WithTimeout(ctx, liveVerifyTimeout)
	defer cancel()

	reachable := make([]bool, len(sources))
	var wg sync.WaitGroup
	sem := make(chan struct{}, liveVerifyParallelism)
	for i := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if err := verifier.VerifyLiveStream(ctx, mode, sources[i]); err != nil {
				mediaLog.Debug("live source failed verification", "url", sources[i].URL, "err", err)
				return
			}
			reachable[i] = true
		}()
	}
	wg.Wait()

	verified := make([]provider.MediaSource, 0, len(sources))
	for i, ok := range reachable {
		if ok {
			verified = append(verified, sources[i])
		}
	}
	if len(verified) == 0 {
		mediaLog.Debug("no live source passed verification; keeping unverified rows", "sources", len(sources))
		return sources
	}
	if dropped := len(sources) - len(verified); dropped > 0 {
		mediaLog.Debug("dropped unreachable live sources", "kept", len(verified), "dropped", dropped)
	}
	return verified
}
