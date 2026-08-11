package history

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// TestStore_ConcurrentMutationsRace exercises Upsert/Delete concurrently
// with -race to guard the async save path in save(): it copies s.items
// under the lock before handing it to a background goroutine specifically
// so that Delete's in-place append(s.items[:i], s.items[i+1:]...) can't
// race with an in-flight marshal of an older snapshot.
func TestStore_ConcurrentMutationsRace(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			key := EntryKey{Title: fmt.Sprintf("show-%d", i%5), Mode: "anime", MediaType: "tv", Season: 1, Episode: i}
			_ = s.Upsert(Entry{Key: key, Title: key.Title, Season: 1, Episode: i, PositionSecs: 10, DurationSecs: 100})
			_ = s.All()
			if i%7 == 0 {
				_ = s.Delete(key)
			}
		})
	}
	wg.Wait()

	// Close must observe every save issued above finishing cleanly.
	s.Close()
}

// TestStore_CloseWaitsForPendingSave confirms Close() actually blocks until
// the async save from the last mutation has written to disk — the fix for
// the "quit right after playback finishes" data-loss window.
func TestStore_CloseWaitsForPendingSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	key := EntryKey{Title: "last-episode", Mode: "anime", MediaType: "tv", Season: 1, Episode: 1}
	if err := s.Upsert(Entry{Key: key, Title: key.Title, Season: 1, Episode: 1, PositionSecs: 90, DurationSecs: 100}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	s.Close()

	// Reopen from disk — if Close() didn't wait for the background write,
	// this would come back empty.
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	if _, ok := reopened.Get(key); !ok {
		t.Fatalf("expected entry to have been persisted before Close() returned")
	}
}
