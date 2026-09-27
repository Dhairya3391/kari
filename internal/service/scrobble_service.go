package service

import (
	"context"
	"sync"
	"time"

	"kari/internal/history"
	"kari/internal/logging"
	"kari/internal/model"
	"kari/internal/provider"
	"kari/internal/scrobble"
)

var scrobbleLog = logging.With("component", "scrobble.service")

// Scrobbler defines the interface for scrobbling media events.
type Scrobbler interface {
	Enqueue(entry history.Entry, media model.ResolvedMedia)
	Close()
}

// ScrobbleService manages asynchronous scrobbling to AniList and Trakt via a bounded queue.
type ScrobbleService struct {
	anilist *scrobble.AniListClient
	trakt   *scrobble.TraktClient
	queue   chan scrobbleTask
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type scrobbleTask struct {
	entry history.Entry
	media model.ResolvedMedia
}

// NewScrobbleService creates and starts a bounded scrobbling worker.
func NewScrobbleService(anilist *scrobble.AniListClient, trakt *scrobble.TraktClient) *ScrobbleService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &ScrobbleService{
		anilist: anilist,
		trakt:   trakt,
		queue:   make(chan scrobbleTask, 32),
		ctx:     ctx,
		cancel:  cancel,
	}

	s.wg.Add(1)
	go s.worker()
	return s
}

// Enqueue queues a scrobble event if the entry is complete.
func (s *ScrobbleService) Enqueue(entry history.Entry, media model.ResolvedMedia) {
	if !entry.Complete {
		return
	}
	select {
	case s.queue <- scrobbleTask{entry: entry, media: media}:
	default:
		scrobbleLog.Warn("scrobble queue full, dropping task", "title", entry.Title)
	}
}

func (s *ScrobbleService) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			// Drain remaining tasks with bounded timeout
			s.drain()
			return
		case task, ok := <-s.queue:
			if !ok {
				return
			}
			s.executeTask(task)
		}
	}
}

func (s *ScrobbleService) executeTask(task scrobbleTask) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if s.anilist != nil && task.entry.Mode == string(provider.ModeAnime) {
		if err := s.anilist.UpdateProgress(ctx, task.media); err != nil {
			scrobbleLog.Debug("anilist scrobble failed", "title", task.entry.Title, "err", err)
		} else {
			scrobbleLog.Info("anilist scrobble success", "title", task.entry.Title)
		}
	}

	if s.trakt != nil {
		var err error
		if task.entry.MediaType == provider.MediaTypeMovie {
			err = s.trakt.ScrobbleMovie(ctx, task.media, 100)
		} else {
			err = s.trakt.ScrobbleEpisode(ctx, task.media, 100)
		}
		if err != nil {
			scrobbleLog.Debug("trakt scrobble failed", "title", task.entry.Title, "err", err)
		} else {
			scrobbleLog.Info("trakt scrobble success", "title", task.entry.Title)
		}
	}
}

func (s *ScrobbleService) drain() {
	for {
		select {
		case task, ok := <-s.queue:
			if !ok {
				return
			}
			s.executeTask(task)
		default:
			return
		}
	}
}

// Close stops the worker and waits for queued scrobbles to finish.
func (s *ScrobbleService) Close() {
	s.cancel()
	s.wg.Wait()
}
