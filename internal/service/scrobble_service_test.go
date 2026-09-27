package service

import (
	"testing"
	"time"

	"kari/internal/history"
	"kari/internal/model"
	"kari/internal/provider"
)

func TestScrobbleService_EnqueueAndDrain(t *testing.T) {
	svc := NewScrobbleService(nil, nil)

	entry := history.Entry{
		Title:     "Test Anime",
		Mode:      string(provider.ModeAnime),
		Complete:  true,
		WatchedAt: time.Now(),
	}
	media := model.ResolvedMedia{
		SeriesTitle: "Test Anime",
	}

	svc.Enqueue(entry, media)
	svc.Close()
}

func TestScrobbleService_IncompleteNotEnqueued(t *testing.T) {
	svc := NewScrobbleService(nil, nil)

	entry := history.Entry{
		Title:     "Test Anime",
		Mode:      string(provider.ModeAnime),
		Complete:  false,
		WatchedAt: time.Now(),
	}
	media := model.ResolvedMedia{
		SeriesTitle: "Test Anime",
	}

	svc.Enqueue(entry, media)
	if len(svc.queue) != 0 {
		t.Errorf("expected queue length 0 for incomplete entry, got %d", len(svc.queue))
	}
	svc.Close()
}
