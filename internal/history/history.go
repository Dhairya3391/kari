package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kari/internal/logging"
	"kari/internal/util"
)

// EntryKey identifies a single watched episode (or movie) purely by what it
// is — title, kind, and position in the series — never by which provider it
// was played from. Providers come and go; a saved watch position shouldn't.
type EntryKey struct {
	Title     string
	Mode      string
	MediaType string
	Season    int
	Episode   int
}

// String renders the canonical persistence key for this entry.
func (k EntryKey) String() string {
	return fmt.Sprintf("%s:%s:%s:s%02de%02d",
		normalizeKeyPart(k.Mode),
		normalizeKeyPart(k.MediaType),
		normalizeKeyPart(k.Title),
		k.Season,
		k.Episode,
	)
}

// Entry records one watch event: what was played, where playback stopped,
// and how far it got. Entries are provider-agnostic by design.
type Entry struct {
	Key             EntryKey  `json:"key"`
	Title           string    `json:"title"`
	EpisodeTitle    string    `json:"episode_title,omitempty"`
	Season          int       `json:"season"`
	Episode         int       `json:"episode"`
	WatchedAt       time.Time `json:"watched_at"`
	PositionSecs    float64   `json:"position_secs"`
	DurationSecs    float64   `json:"duration_secs"`
	PercentComplete float64   `json:"percent_complete"`
	Complete        bool      `json:"complete"`

	// Metadata for scrobble idempotency
	LastScrobbledPercent float64 `json:"last_scrobbled_percent,omitempty"`

	// Metadata for re-play — deliberately provider-agnostic. Resuming
	// re-searches whichever providers are currently registered by Title/
	// TMDBID rather than trusting a stored provider name or URL, so history
	// keeps working even if providers are added, removed, or renamed later.
	Mode      string `json:"mode,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	TMDBID    int    `json:"tmdb_id,omitempty"`
}

// GroupKey collapses entries into one series/movie for the history list.
type GroupKey struct {
	Mode      string
	MediaType string
	Title     string
}

// String renders the canonical grouping key.
func (k GroupKey) String() string {
	return fmt.Sprintf("%s:%s:%s",
		normalizeKeyPart(k.Mode),
		normalizeKeyPart(k.MediaType),
		normalizeKeyPart(k.Title),
	)
}

// Group is the history-list projection of every entry sharing a GroupKey:
// where to resume, how far has been completed, and the raw entries.
type Group struct {
	Key              GroupKey
	Title            string
	Mode             string
	MediaType        string
	LastPlayed       Entry
	ContinueEntry    Entry
	FarthestComplete Entry
	WatchedCount     int
	Entries          []Entry
	HasIncomplete    bool
	HasComplete      bool
}

// BuildGroups aggregates flat entries into display groups, newest-first.
func BuildGroups(entries []Entry) []Group {
	groupsByKey := make(map[string]*Group)
	order := make([]string, 0, len(entries))

	for _, entry := range entries {
		key := groupKeyForEntry(entry)
		keyStr := key.String()
		group, ok := groupsByKey[keyStr]
		if !ok {
			group = &Group{
				Key:        key,
				Title:      entry.Title,
				Mode:       entry.Mode,
				MediaType:  entry.MediaType,
				LastPlayed: entry,
			}
			groupsByKey[keyStr] = group
			order = append(order, keyStr)
		}

		group.Entries = append(group.Entries, entry)
		if entry.WatchedAt.After(group.LastPlayed.WatchedAt) {
			group.LastPlayed = entry
		}
		if group.Title == "" {
			group.Title = entry.Title
		}
		if group.Mode == "" {
			group.Mode = entry.Mode
		}
		if group.MediaType == "" {
			group.MediaType = entry.MediaType
		}

		if entry.Complete {
			group.HasComplete = true
			group.WatchedCount++
			if !isSeriesContent(group.FarthestComplete) || entryAfter(entry, group.FarthestComplete) {
				group.FarthestComplete = entry
			}
			continue
		}

		if entry.PositionSecs > 5 || entry.PercentComplete > 0 {
			if !group.HasIncomplete || entry.WatchedAt.After(group.ContinueEntry.WatchedAt) {
				group.ContinueEntry = entry
			}
			group.HasIncomplete = true
		}
	}

	groups := make([]Group, 0, len(order))
	for _, key := range order {
		group := groupsByKey[key]
		if group.HasIncomplete && group.HasComplete && entryAfter(group.FarthestComplete, group.ContinueEntry) {
			group.HasIncomplete = false
		}
		if !group.HasIncomplete {
			if isSeriesContent(group.FarthestComplete) {
				group.ContinueEntry = group.FarthestComplete
			} else {
				group.ContinueEntry = group.LastPlayed
			}
		}
		groups = append(groups, *group)
	}

	return groups
}

// BuildGroupLookup maps group key strings to their keys, for O(1) lookups
// when the TUI resolves a selected row back to a series.
func BuildGroupLookup(entries []Entry) map[string]GroupKey {
	groups := BuildGroups(entries)
	lookup := make(map[string]GroupKey, len(groups))
	for _, group := range groups {
		lookup[group.Key.String()] = group.Key
	}
	return lookup
}

// Store persists watch-history entries to disk as JSON with atomic
// rewrites, and serves reads from memory.
type Store struct {
	path   string
	mu     sync.Mutex
	items  []Entry
	saveWG sync.WaitGroup
}

// storageFormat wraps the on-disk document shape (versioned for future
// migrations).
type storageFormat struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// NewStore loads (or initializes) the history file at path.
func NewStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}

	s := &Store{
		path:  path,
		items: []Entry{},
	}

	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}

		var format storageFormat
		if err := json.Unmarshal(data, &format); err != nil {
			return nil, fmt.Errorf("malformed history file: %w", err)
		}
		s.items = deduplicate(format.Entries)
	}

	return s, nil
}

// deduplicate keeps only the latest entry per EntryKey, preserving order.
func deduplicate(entries []Entry) []Entry {
	seen := make(map[string]bool)
	var unique []Entry
	for _, e := range entries {
		k := e.Key.String()
		if !seen[k] {
			seen[k] = true
			unique = append(unique, e)
		}
	}
	return unique
}

// Upsert inserts or replaces the entry with e's key and persists.
func (s *Store) Upsert(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e.DurationSecs > 0 {
		e.PercentComplete = e.PositionSecs / e.DurationSecs
	}
	if e.PercentComplete > 0.85 {
		e.Complete = true
	} else if e.PercentComplete > 0 {
		// If they didn't manually set it to true and it's < 85%, ensure it's false
		if e.DurationSecs > 1 {
			e.Complete = false
		}
	}

	keyStr := e.Key.String()
	var existingLastScrobbled float64
	var foundExisting bool
	var newItems []Entry
	for _, item := range s.items {
		if item.Key.String() == keyStr {
			if !foundExisting {
				existingLastScrobbled = item.LastScrobbledPercent
				foundExisting = true
			}
		} else {
			newItems = append(newItems, item)
		}
	}

	if e.LastScrobbledPercent == 0 && foundExisting {
		e.LastScrobbledPercent = existingLastScrobbled
	}

	s.items = append([]Entry{e}, newItems...)

	return s.save()
}

// Get returns the stored entry for key.
func (s *Store) Get(key EntryKey) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	keyStr := key.String()
	for _, item := range s.items {
		if item.Key.String() == keyStr {
			return item, true
		}
	}
	return Entry{}, false
}

// Delete removes a single entry and persists.
func (s *Store) Delete(key EntryKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	keyStr := key.String()
	for i, item := range s.items {
		if item.Key.String() == keyStr {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return s.save()
		}
	}
	return nil
}

// DeleteGroup removes every entry in the group and persists.
func (s *Store) DeleteGroup(key GroupKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	keyStr := key.String()
	newItems := make([]Entry, 0, len(s.items))
	for _, item := range s.items {
		if groupKeyForEntry(item).String() != keyStr {
			newItems = append(newItems, item)
		}
	}
	if len(newItems) == len(s.items) {
		return nil
	}
	s.items = newItems
	return s.save()
}

// All returns a snapshot of all stored entries.
func (s *Store) All() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	res := make([]Entry, len(s.items))
	copy(res, s.items)
	return res
}

// Clear wipes all history and persists.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items = []Entry{}
	return s.save()
}

// save persists the store to disk. The actual marshal+write happens on a
// background goroutine so callers (in particular the TUI's Update loop,
// which calls Upsert synchronously on every playback event) never block on
// disk I/O. entries is copied here, under the caller's lock, rather than
// captured by reference — some callers (Delete) mutate s.items in place via
// append-in-place, which would otherwise race with the goroutine's read of
// the same backing array.
func (s *Store) save() error {
	entries := make([]Entry, len(s.items))
	copy(entries, s.items)
	path := s.path

	s.saveWG.Go(func() {
		format := storageFormat{
			Version: 1,
			Entries: entries,
		}

		data, err := json.MarshalIndent(format, "", "  ")
		if err != nil {
			logging.Error("history marshal failed", "err", err)
			return
		}

		if err := util.AtomicWriteFile(path, data, 0644); err != nil {
			logging.Error("history write failed", "path", path, "err", err)
		}
	})

	return nil
}

// Close blocks until any in-flight background save has finished writing to
// disk. Call it during shutdown, after the TUI has exited, so the most
// recent watch-progress update (e.g. from an onPlayDone right before the
// user quits) isn't lost to the process exiting before its async save
// completes.
func (s *Store) Close() {
	s.saveWG.Wait()
}

func groupKeyForEntry(entry Entry) GroupKey {
	return GroupKey{
		Mode:      entry.Mode,
		MediaType: entry.MediaType,
		Title:     FirstNonEmpty(entry.Title, entry.Key.Title),
	}
}

func normalizeKeyPart(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// FirstNonEmpty returns the first value that isn't empty after trimming.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func entryAfter(candidate, current Entry) bool {
	if candidate.Season != current.Season {
		return candidate.Season > current.Season
	}
	if candidate.Episode != current.Episode {
		return candidate.Episode > current.Episode
	}
	return candidate.WatchedAt.After(current.WatchedAt)
}

func isSeriesContent(entry Entry) bool {
	return entry.Season > 0 || entry.Episode > 0
}
