package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kari/internal/model"
	"kari/internal/provider"
)

type resolutionCallStub struct {
	stubProvider
	resolve  func(context.Context) ([]provider.MediaSource, error)
	deadline time.Duration
}

// Deadlines allows tests to exercise provider budgets separately from cancellation.
func (p *resolutionCallStub) Deadlines(provider.ContentType) provider.Deadlines {
	return provider.Deadlines{Source: p.deadline}
}

// ResolveSource permits tests to model providers that ignore cancellation.
func (p *resolutionCallStub) ResolveSource(ctx context.Context, _ string, _ provider.Episode) ([]provider.MediaSource, error) {
	return p.resolve(ctx)
}

type resolutionStreamStub struct {
	resolutionCallStub
	stream func(context.Context, chan<- []provider.MediaSource) error
}

// ResolveStream permits tests to control individual batches and terminal errors.
func (p *resolutionStreamStub) ResolveStream(ctx context.Context, _ string, _ provider.Episode, updates chan<- []provider.MediaSource) error {
	return p.stream(ctx, updates)
}

type resolutionOutcome struct {
	media model.ResolvedMedia
	err   error
}

func awaitResolutionSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resolve signal")
	}
}

// TestResolveSerialPublication keeps a callback blocked while another provider
// finishes: neither overlapping delivery nor a regressing snapshot is allowed.
func TestResolveSerialPublication(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			secondReturned := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()

			first := &resolutionStreamStub{
				resolutionCallStub: resolutionCallStub{stubProvider: stubProvider{name: "first", mode: provider.ModeMovies}},
				stream: func(_ context.Context, updates chan<- []provider.MediaSource) error {
					updates <- []provider.MediaSource{{URL: "https://cdn.example/first.mp4"}}
					return nil
				},
			}
			second := &resolutionCallStub{
				stubProvider: stubProvider{name: "second", mode: provider.ModeMovies},
				resolve: func(context.Context) ([]provider.MediaSource, error) {
					<-entered
					defer close(secondReturned)
					return []provider.MediaSource{{URL: "https://cdn.example/second.mp4"}}, nil
				},
			}
			var secondProvider provider.Provider = second
			if streaming {
				secondProvider = &resolutionStreamStub{
					resolutionCallStub: *second,
					stream: func(_ context.Context, updates chan<- []provider.MediaSource) error {
						<-entered
						updates <- []provider.MediaSource{{URL: "https://cdn.example/second.mp4"}}
						close(secondReturned)
						return nil
					},
				}
			}
			svc := NewMediaService(newTestRegistry(first, secondProvider))
			var active, calls atomic.Int32
			// At most two source and two completion publications are expected.
			snapshots := make(chan model.ResolvedMedia, 4)
			overlap := make(chan struct{}, 1)
			callback := func(media model.ResolvedMedia) {
				if active.Add(1) > 1 {
					select {
					case overlap <- struct{}{}:
					default:
					}
				}
				defer active.Add(-1)
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				snapshots <- media
			}
			// One terminal result lets cleanup release a failed test's worker.
			done := make(chan resolutionOutcome, 1)
			go func() {
				media, err := svc.Resolve(context.Background(), provider.ModeMovies,
					provider.SearchResult{ID: "1"}, provider.Episode{}, callback, ResolveOptions{})
				done <- resolutionOutcome{media: media, err: err}
			}()
			awaitResolutionSignal(t, entered)
			awaitResolutionSignal(t, secondReturned)
			select {
			case <-overlap:
				t.Error("callbacks overlapped while the first publication was blocked")
			case <-time.After(40 * time.Millisecond):
			}
			unblock()
			select {
			case result := <-done:
				if result.err != nil || len(result.media.Playback) != 2 {
					t.Fatalf("resolve = %+v, %v", result.media, result.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("resolve did not finish after releasing callback")
			}
			close(snapshots)
			var sourceCount, completedCount int
			for snapshot := range snapshots {
				if len(snapshot.Playback) < sourceCount || len(snapshot.CompletedResolvers) < completedCount {
					t.Fatalf("snapshot regressed: %+v", snapshot)
				}
				sourceCount = len(snapshot.Playback)
				completedCount = len(snapshot.CompletedResolvers)
			}
			if sourceCount != 2 || completedCount != 2 {
				t.Fatalf("final publication has %d sources, %d completed providers", sourceCount, completedCount)
			}
		})
	}
}

// TestResolvePublishesEnrichedSnapshots keeps the "Auto" placeholder off the
// screen and shows results as they finish: quality/bitrate arrive from the
// adaptive probe, audio labels arrive just after from the non-blocking pass.
func TestResolvePublishesEnrichedSnapshots(t *testing.T) {
	var entered sync.Once
	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered.Do(func() { close(probeEntered) })
		<-releaseProbe
		fmt.Fprint(w, "#EXTM3U\n"+
			"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"English\",LANGUAGE=\"en\"\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=23000000,RESOLUTION=3840x2160,CODECS=\"hvc1.2.4.L153.B0\"\n"+
			"video.m3u8\n")
	}))
	defer server.Close()
	defer func() {
		select {
		case <-releaseProbe:
		default:
			close(releaseProbe)
		}
	}()

	fast := &stubProvider{
		name: "fast", mode: provider.ModeMovies,
		sources: []provider.MediaSource{{
			URL: server.URL + "/master.m3u8", Type: provider.SourceTypeHLS, Quality: "Auto",
		}},
	}
	svc := NewMediaService(newTestRegistry(fast))
	snapshots := make(chan model.ResolvedMedia, 16)
	done := make(chan resolutionOutcome, 1)
	go func() {
		media, err := svc.Resolve(context.Background(), provider.ModeMovies,
			provider.SearchResult{ID: "1"}, provider.Episode{}, func(snapshot model.ResolvedMedia) {
				snapshots <- snapshot
			}, ResolveOptions{})
		done <- resolutionOutcome{media: media, err: err}
	}()

	awaitResolutionSignal(t, probeEntered)
	select {
	case <-snapshots:
		t.Fatal("snapshot was published before the playlist probe finished")
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseProbe)

	sawQuality, sawAudio := false, false
	deadline := time.After(2 * time.Second)
	for !sawQuality || !sawAudio {
		select {
		case snapshot := <-snapshots:
			if len(snapshot.Playback) != 1 {
				t.Fatalf("snapshot playback = %+v, want one source", snapshot.Playback)
			}
			src := snapshot.Playback[0]
			if src.Quality == "4K (2160p)" && src.Bitrate == 23_000_000 {
				sawQuality = true
			}
			if src.AudioLabel == "English" {
				sawAudio = true
			}
		case <-deadline:
			t.Fatalf("enriched snapshots missing (quality=%v audio=%v)", sawQuality, sawAudio)
		}
	}
	select {
	case result := <-done:
		if result.err != nil || len(result.media.Playback) != 1 {
			t.Fatalf("resolve = %+v, %v", result.media, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resolve did not finish after releasing the probe")
	}
}

// TestResolveCollectsStaggeredProviders verifies that slower providers still
// contribute when earlier ones have already delivered sources.
func TestResolveCollectsStaggeredProviders(t *testing.T) {
	delays := []time.Duration{200 * time.Millisecond, 2500 * time.Millisecond, 4 * time.Second, 5500 * time.Millisecond}
	provs := make([]provider.Provider, 0, len(delays))
	for i, delay := range delays {
		provs = append(provs, &resolutionCallStub{
			stubProvider: stubProvider{name: fmt.Sprintf("p%d", i), mode: provider.ModeMovies},
			resolve: func(ctx context.Context) ([]provider.MediaSource, error) {
				select {
				case <-time.After(delay):
					return []provider.MediaSource{{URL: "https://cdn.example/" + fmt.Sprint(delay) + ".mp4"}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		})
	}
	svc := NewMediaService(newTestRegistry(provs...))
	start := time.Now()
	media, err := svc.Resolve(context.Background(), provider.ModeMovies,
		provider.SearchResult{ID: "1"}, provider.Episode{}, nil, ResolveOptions{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	t.Logf("OBSERVED: elapsed=%v sources=%d (providers=%d)", elapsed, len(media.Playback), len(provs))
	if len(media.Playback) < len(provs) {
		t.Errorf("only %d of %d providers contributed", len(media.Playback), len(provs))
	}
}

// TestResolveDoesNotBlameProviderThatDelivered verifies that a provider which
// delivered sources is not reported as a failure when its stream later ends.
func TestResolveDoesNotBlameProviderThatDelivered(t *testing.T) {
	winner := &resolutionStreamStub{
		resolutionCallStub: resolutionCallStub{
			stubProvider: stubProvider{name: "winner", mode: provider.ModeMovies},
			deadline:     300 * time.Millisecond,
		},
		stream: func(ctx context.Context, updates chan<- []provider.MediaSource) error {
			updates <- []provider.MediaSource{{URL: "https://cdn.example/winner.mp4"}}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	svc := NewMediaService(newTestRegistry(winner))
	if _, err := svc.Resolve(context.Background(), provider.ModeMovies,
		provider.SearchResult{ID: "1"}, provider.Episode{}, nil, ResolveOptions{}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, name := range svc.LastFailures() {
		if strings.EqualFold(name, "winner") {
			t.Fatalf("provider that delivered sources was recorded as a failure: %v", svc.LastFailures())
		}
	}
}

// TestResolveBoundsUncooperativeProviders preserves partial success and returns
// on cancellation even when direct or streaming calls have not returned.
func TestResolveBoundsUncooperativeProviders(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("streaming=%v/partial=%v", streaming, partial), func(t *testing.T) {
				started := make(chan struct{})
				release := make(chan struct{})
				returned := make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				defer unblock()
				stalled := &resolutionCallStub{
					stubProvider: stubProvider{name: "stalled", mode: provider.ModeMovies},
					resolve: func(context.Context) ([]provider.MediaSource, error) {
						close(started)
						<-release
						defer close(returned)
						return nil, provider.ErrAudioUnavailable
					},
				}
				var stalledProvider provider.Provider = stalled
				if streaming {
					stalledProvider = &resolutionStreamStub{
						resolutionCallStub: *stalled,
						stream: func(context.Context, chan<- []provider.MediaSource) error {
							close(started)
							<-release
							defer close(returned)
							return provider.ErrAudioUnavailable
						},
					}
				}
				providers := []provider.Provider{stalledProvider}
				if partial {
					providers = append(providers, &stubProvider{
						name: "good", mode: provider.ModeMovies,
						sources: []provider.MediaSource{{URL: "https://cdn.example/good.mp4"}},
					})
				}
				svc := NewMediaService(newTestRegistry(providers...))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var callbackCount atomic.Int32
				callback := func(media model.ResolvedMedia) {
					callbackCount.Add(1)
					if len(media.Playback) > 0 {
						cancel()
					}
				}
				// A single result avoids leaving a test consumer behind on failure.
				done := make(chan resolutionOutcome, 1)
				go func() {
					media, err := svc.Resolve(ctx, provider.ModeMovies,
						provider.SearchResult{ID: "1"}, provider.Episode{}, callback, ResolveOptions{})
					done <- resolutionOutcome{media: media, err: err}
				}()
				awaitResolutionSignal(t, started)
				if !partial {
					cancel()
				}
				select {
				case result := <-done:
					if partial {
						if result.err != nil || len(result.media.Playback) != 1 || len(result.media.CompletedResolvers) != 2 {
							t.Fatalf("partial resolve = %+v, %v", result.media, result.err)
						}
					} else if !errors.Is(result.err, context.Canceled) {
						t.Fatalf("resolve error = %v, want cancellation", result.err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("resolve waited for an uncooperative provider")
				}
				before := callbackCount.Load()
				unblock()
				awaitResolutionSignal(t, returned)
				if callbackCount.Load() != before {
					t.Fatal("callback occurred after resolve returned")
				}
			})
		}
	}
}

// TestResolveProviderDeadline bounds an uncooperative provider independently of
// the caller deadline, while retaining sources from the successful provider.
func TestResolveProviderDeadline(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			release := make(chan struct{})
			defer close(release)
			stalled := &resolutionCallStub{
				stubProvider: stubProvider{name: "stalled", mode: provider.ModeMovies},
				deadline:     30 * time.Millisecond,
				resolve: func(context.Context) ([]provider.MediaSource, error) {
					<-release
					return nil, provider.ErrNoSources
				},
			}
			var p provider.Provider = stalled
			if streaming {
				p = &resolutionStreamStub{
					resolutionCallStub: *stalled,
					stream: func(context.Context, chan<- []provider.MediaSource) error {
						<-release
						return provider.ErrNoSources
					},
				}
			}
			good := &stubProvider{name: "good", mode: provider.ModeMovies,
				sources: []provider.MediaSource{{URL: "https://cdn.example/good.mp4"}}}
			svc := NewMediaService(newTestRegistry(p, good))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			result, err := svc.Resolve(ctx, provider.ModeMovies,
				provider.SearchResult{ID: "1"}, provider.Episode{}, nil, ResolveOptions{})
			if err != nil || len(result.Playback) != 1 || ctx.Err() != nil {
				t.Fatalf("provider budget did not preserve partial success: %+v, %v, caller=%v", result, err, ctx.Err())
			}
			if failed := svc.LastFailures(); len(failed) != 1 || failed[0] != "stalled" {
				t.Fatalf("failed providers = %v, want stalled", failed)
			}
		})
	}
}

// TestResolvePreservesSourcesWithTerminalError ensures a final provider error
// never discards an otherwise usable batch, whether direct or incremental.
func TestResolvePreservesSourcesWithTerminalError(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			batch := []provider.MediaSource{{URL: "https://cdn.example/partial.mp4"}}
			direct := &resolutionCallStub{
				stubProvider: stubProvider{name: "partial", mode: provider.ModeMovies},
				resolve: func(context.Context) ([]provider.MediaSource, error) {
					return batch, provider.ErrNoSources
				},
			}
			var p provider.Provider = direct
			if streaming {
				p = &resolutionStreamStub{
					resolutionCallStub: *direct,
					stream: func(_ context.Context, updates chan<- []provider.MediaSource) error {
						updates <- batch
						return provider.ErrNoSources
					},
				}
			}
			svc := NewMediaService(newTestRegistry(p))
			result, err := svc.Resolve(context.Background(), provider.ModeMovies,
				provider.SearchResult{ID: "1"}, provider.Episode{}, nil, ResolveOptions{})
			if err != nil || len(result.Playback) != 1 || len(result.CompletedResolvers) != 1 {
				t.Fatalf("partial result lost: %+v, %v", result, err)
			}
			if failed := svc.LastFailures(); len(failed) != 1 || failed[0] != "partial" {
				t.Fatalf("failed providers = %v, want partial", failed)
			}
		})
	}
}

// TestSourceResolutionLateReturn verifies terminal reporting never needs an
// abandoned consumer, including streams that finish only after cancellation.
func TestSourceResolutionLateReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	p := &resolutionStreamStub{
		stream: func(context.Context, chan<- []provider.MediaSource) error {
			close(started)
			<-release
			return provider.ErrNoSources
		},
	}
	updates, finished := startSourceResolution(ctx, p, "1", provider.Episode{})
	awaitResolutionSignal(t, started)
	cancel()
	close(release)
	select {
	case err := <-finished:
		if !errors.Is(err, provider.ErrNoSources) {
			t.Fatalf("terminal error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late provider could not report terminal result")
	}
	select {
	case _, open := <-updates:
		if open {
			t.Fatal("unexpected stream update")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late provider did not close updates")
	}
}

// TestResolveProviderDeadlineBoundsManifestProbe proves post-resolve HLS
// probing honours the provider's own source deadline instead of the much
// longer overall resolve budget.
func TestResolveProviderDeadlineBoundsManifestProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=1920x1080\nsegment.m3u8\n")
	}))
	defer server.Close()

	p := &resolutionCallStub{
		stubProvider: stubProvider{name: "slow-probe", mode: provider.ModeMovies},
		deadline:     150 * time.Millisecond,
		resolve: func(context.Context) ([]provider.MediaSource, error) {
			return []provider.MediaSource{{
				URL:     server.URL + "/master.m3u8",
				Quality: "Auto",
				Type:    provider.SourceTypeHLS,
			}}, nil
		},
	}
	svc := NewMediaService(newTestRegistry(p))
	start := time.Now()
	if _, err := svc.Resolve(context.Background(), provider.ModeMovies,
		provider.SearchResult{ID: "1"}, provider.Episode{}, nil, ResolveOptions{}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("resolve took %v, want the 150ms provider deadline to bound the probe", elapsed)
	}
}

// TestResolveSkipsRedundantBatches still publishes completion after skipping
// empty and duplicate stream batches, and keeps nested callback data detached.
func TestResolveSkipsRedundantBatches(t *testing.T) {
	batch := []provider.MediaSource{{
		URL: "https://cdn.example/video.mp4", ExtraArgs: []string{"--original"},
		Subtitles: []provider.SubtitleOption{{URL: "https://cdn.example/en.vtt", Language: "en"}},
		SkipTimes: &provider.SkipTimes{OpEnd: 90},
	}}
	p := &resolutionStreamStub{
		resolutionCallStub: resolutionCallStub{stubProvider: stubProvider{name: "stream", mode: provider.ModeMovies}},
		stream: func(_ context.Context, updates chan<- []provider.MediaSource) error {
			updates <- nil
			updates <- batch
			updates <- batch
			updates <- []provider.MediaSource{{URL: ""}}
			return nil
		},
	}
	svc := NewMediaService(newTestRegistry(p))
	var calls int
	callback := func(media model.ResolvedMedia) {
		calls++
		media.Playback[0].ExtraArgs[0] = "changed"
		media.Playback[0].Subtitles[0].URL = "changed"
		media.Playback[0].SkipTimes.OpEnd = 1
		media.Subtitles[0].URL = "changed"
	}
	result, err := svc.Resolve(context.Background(), provider.ModeMovies,
		provider.SearchResult{ID: "1"}, provider.Episode{}, callback, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("publications = %d, want source and completion only", calls)
	}
	if result.Playback[0].ExtraArgs[0] != "--original" ||
		result.Playback[0].Subtitles[0].URL != "https://cdn.example/en.vtt" ||
		result.Playback[0].SkipTimes.OpEnd != 90 || result.Subtitles[0].URL != "https://cdn.example/en.vtt" {
		t.Fatalf("callback mutated later result: %+v", result)
	}
}

// TestResolveRetryNeverUsesDisabledProviders proves a disabled provider is
// never queried, on a full resolve, an excluded retry, or a Retry-only call.
func TestResolveRetryNeverUsesDisabledProviders(t *testing.T) {
	series := provider.SearchResult{Title: "T", ID: "21", Provider: "miruro", Type: provider.ModeAnime}
	episode := provider.Episode{ID: "watch/miruro/21/sub/1180", Episode: 1180, Audio: provider.AudioSub}

	t.Run("exclude", func(t *testing.T) {
		var disabledCalls int
		winner := &resolveStub{name: "miruro", mode: provider.ModeAnime,
			sources: []provider.MediaSource{{URL: "https://cdn.example.com/miruro.m3u8", Quality: "1080p"}}}
		failing := &resolveStub{name: "failing", mode: provider.ModeAnime, err: provider.ErrTimeout}
		disabled := &countStub{
			resolveStub: resolveStub{name: "anicine", mode: provider.ModeAnime,
				sources: []provider.MediaSource{{URL: "https://cdn.example.com/anicine.m3u8", Quality: "1080p"}}},
			calls: &disabledCalls,
		}

		svc := NewMediaService(newTestRegistry(winner, failing, disabled))
		svc.SetDisabledProviders([]string{"anime:anicine"})

		if _, err := svc.Resolve(context.Background(), provider.ModeAnime, series, episode, nil, ResolveOptions{}); err != nil {
			t.Fatalf("seed resolve: %v", err)
		}
		if disabledCalls != 0 {
			t.Fatalf("full resolve queried disabled provider %d times, want 0", disabledCalls)
		}

		// The retry excludes the winner and the re-queried provider fails again,
		// leaving this call's aggregator empty. The disabled fallback must stay off.
		resolved, _ := svc.Resolve(context.Background(), provider.ModeAnime, series, episode, nil,
			ResolveOptions{Exclude: []string{"miruro"}})
		if disabledCalls != 0 {
			t.Errorf("retry queried disabled provider %d times, want 0", disabledCalls)
		}
		for _, src := range resolved.Playback {
			if src.Resolver == "anicine" {
				t.Errorf("retry merged a disabled provider's source: %+v", src)
			}
		}
	})

	t.Run("retry", func(t *testing.T) {
		var disabledCalls int
		failing := &resolveStub{name: "failing", mode: provider.ModeAnime, err: provider.ErrTimeout}
		disabled := &countStub{
			resolveStub: resolveStub{name: "anicine", mode: provider.ModeAnime,
				sources: []provider.MediaSource{{URL: "https://cdn.example.com/anicine.m3u8", Quality: "1080p"}}},
			calls: &disabledCalls,
		}

		svc := NewMediaService(newTestRegistry(failing, disabled))
		svc.SetDisabledProviders([]string{"anime:anicine"})

		// A full resolve must not revive a disabled provider either.
		_, err := svc.Resolve(context.Background(), provider.ModeAnime, series, episode, nil, ResolveOptions{})
		if err == nil {
			t.Fatal("seed resolve unexpectedly succeeded with only a disabled provider")
		}
		if disabledCalls != 0 {
			t.Fatalf("full resolve queried disabled provider %d times, want 0", disabledCalls)
		}

		// A Retry-only call must not fall back either.
		if _, err := svc.Resolve(context.Background(), provider.ModeAnime, series, episode, nil,
			ResolveOptions{Retry: []string{"failing"}}); err == nil {
			t.Fatal("retry unexpectedly succeeded with only a disabled provider")
		}
		if disabledCalls != 0 {
			t.Errorf("retry queried disabled provider %d times, want 0", disabledCalls)
		}
	})
}
