package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"kari/internal/provider"
)

type fakeLiveVerifier struct {
	mu     sync.Mutex
	verify func(provider.MediaSource) error
	mode   provider.ContentType
}

func (f *fakeLiveVerifier) VerifyLiveStream(_ context.Context, mode provider.ContentType, source provider.MediaSource) error {
	f.mu.Lock()
	f.mode = mode
	f.mu.Unlock()
	return f.verify(source)
}

func (f *fakeLiveVerifier) verifiedMode() provider.ContentType {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mode
}

func TestVerifyLiveSourcesKeepsReachableRows(t *testing.T) {
	verifier := &fakeLiveVerifier{verify: func(source provider.MediaSource) error {
		if source.URL == "https://dead.example/live.m3u8" {
			return errors.New("unreachable")
		}
		return nil
	}}
	sources := []provider.MediaSource{
		{URL: "https://one.example/live.m3u8"},
		{URL: "https://dead.example/live.m3u8"},
		{URL: "https://two.example/live.m3u8"},
	}

	kept := verifyLiveSources(context.Background(), provider.ModeLive, verifier, sources)
	if len(kept) != 2 {
		t.Fatalf("kept = %d, want 2: %+v", len(kept), kept)
	}
	if kept[0].URL != sources[0].URL || kept[1].URL != sources[2].URL {
		t.Errorf("order not preserved: %+v", kept)
	}
	if got := verifier.verifiedMode(); got != provider.ModeLive {
		t.Errorf("mode = %q, want live", got)
	}
}

func TestVerifyLiveSourcesKeepsAllWhenNothingVerifies(t *testing.T) {
	verifier := &fakeLiveVerifier{verify: func(provider.MediaSource) error {
		return errors.New("unreachable")
	}}
	sources := []provider.MediaSource{
		{URL: "https://one.example/live.m3u8"},
		{URL: "https://two.example/live.m3u8"},
	}

	kept := verifyLiveSources(context.Background(), provider.ModeLive, verifier, sources)
	if len(kept) != len(sources) {
		t.Fatalf("kept = %d, want all %d", len(kept), len(sources))
	}
}

func TestVerifyLiveSourcesEmptyBatch(t *testing.T) {
	verifier := &fakeLiveVerifier{verify: func(provider.MediaSource) error {
		t.Fatal("empty batch must not be probed")
		return nil
	}}
	if got := verifyLiveSources(context.Background(), provider.ModeLive, verifier, nil); len(got) != 0 {
		t.Errorf("got %d sources, want none", len(got))
	}
}
