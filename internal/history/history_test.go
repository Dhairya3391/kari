package history

import (
	"fmt"
	"os"
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

// TestStore_AudioModeAndLanguagePersistence verifies that AudioMode ("sub"/"dub")
// and playback source Language ("Hindi", "English", etc.) are saved and restored
// across store reopens.
func TestStore_AudioModeAndLanguagePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	key := EntryKey{Title: "frieren", Mode: "anime", MediaType: "tv", Season: 1, Episode: 5}
	entry := Entry{
		Key:          key,
		Title:        "frieren",
		Season:       1,
		Episode:      5,
		PositionSecs: 500,
		DurationSecs: 1400,
		AudioMode:    "dub",
		Language:     "English",
	}
	if err := s.Upsert(entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	s.Close()

	reopened, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}

	got, ok := reopened.Get(key)
	if !ok {
		t.Fatalf("expected entry to exist after reopen")
	}
	if got.AudioMode != "dub" {
		t.Errorf("AudioMode = %q, want %q", got.AudioMode, "dub")
	}
	if got.Language != "English" {
		t.Errorf("Language = %q, want %q", got.Language, "English")
	}

	groups := BuildGroups(reopened.All())
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].ContinueEntry.AudioMode != "dub" {
		t.Errorf("Group ContinueEntry.AudioMode = %q, want %q", groups[0].ContinueEntry.AudioMode, "dub")
	}
	if groups[0].ContinueEntry.Language != "English" {
		t.Errorf("Group ContinueEntry.Language = %q, want %q", groups[0].ContinueEntry.Language, "English")
	}
}

func TestStore_LoadLegacyV1Format(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "history.json")
	legacyJSON := `[
  {
    "key": {
      "Title": "Frieren",
      "Mode": "anime",
      "MediaType": "tv",
      "Season": 1,
      "Episode": 1
    },
    "title": "Frieren: Beyond Journey's End",
    "episode_title": "The Journey's End",
    "season": 1,
    "episode": 1,
    "watched_at": "2026-09-20T12:00:00Z",
    "position_secs": 1200.5,
    "duration_secs": 1420.0,
    "percent_complete": 84.5,
    "complete": false,
    "mode": "anime",
    "media_type": "tv",
    "tmdb_id": 209867,
    "audio_mode": "sub",
    "language": "Japanese"
  }
]`
	if err := os.WriteFile(histPath, []byte(legacyJSON), 0644); err != nil {
		t.Fatalf("WriteFile legacy json: %v", err)
	}

	s, err := NewStore(histPath)
	if err != nil {
		t.Fatalf("NewStore on legacy json: %v", err)
	}
	defer s.Close()

	entries := s.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Title != "Frieren: Beyond Journey's End" || e.Episode != 1 || e.PositionSecs != 1200.5 {
		t.Errorf("unexpected entry values: %+v", e)
	}
}

// TestStore_MangaMigration verifies that a v1 manga entry with PositionSecs=page,
// DurationSecs=totalPages, EpisodeTitle=chapter migrates to explicit fields.
func TestStore_MangaMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")

	// Write a v1-style manga entry (no version wrapper, old field encoding)
	legacyJSON := `[{"key":{"Title":"One Piece","Mode":"manga","MediaType":"manga","Season":0,"Episode":0},"title":"One Piece","episode_title":"123","season":0,"episode":0,"watched_at":"2024-01-01T00:00:00Z","position_secs":5,"duration_secs":57,"percent_complete":0.08,"complete":false,"mode":"manga","media_type":"manga"}]`
	if err := os.WriteFile(path, []byte(legacyJSON), 0600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	entries := store.All()
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.MangaChapter != "123" {
		t.Errorf("MangaChapter = %q, want %q", e.MangaChapter, "123")
	}
	if e.MangaPage != 5 {
		t.Errorf("MangaPage = %d, want 5", e.MangaPage)
	}
	if e.MangaPages != 57 {
		t.Errorf("MangaPages = %d, want 57", e.MangaPages)
	}

	// Verify .bak was created
	bakPath := path + ".bak"
	if _, err := os.Stat(bakPath); os.IsNotExist(err) {
		t.Error("expected history.json.bak to be created on first migration")
	}
}
