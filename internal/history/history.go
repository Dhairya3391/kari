package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type EntryKey struct {
	Provider string
	Title    string
	Season   int
	Episode  int
}

func (k EntryKey) String() string {
	return fmt.Sprintf("%s:%s:s%02de%02d",
		strings.ToLower(strings.TrimSpace(k.Provider)),
		strings.ToLower(strings.TrimSpace(k.Title)),
		k.Season,
		k.Episode,
	)
}

type Entry struct {
	Key             EntryKey  `json:"key"`
	ProviderName    string    `json:"provider_name"`
	Title           string    `json:"title"`
	EpisodeTitle    string    `json:"episode_title"`
	Season          int       `json:"season"`
	Episode         int       `json:"episode"`
	WatchedAt       time.Time `json:"watched_at"`
	PositionSecs    float64   `json:"position_secs"`
	DurationSecs    float64   `json:"duration_secs"`
	PercentComplete float64   `json:"percent_complete"`
	Complete        bool      `json:"complete"`

	// Metadata for scrobble idempotency
	LastScrobbledPercent float64 `json:"last_scrobbled_percent,omitempty"`

	// Metadata for re-play
	Mode       string `json:"mode,omitempty"`
	SeriesURL  string `json:"series_url,omitempty"`
	EpisodeURL string `json:"episode_url,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	TMDBID     int    `json:"tmdb_id,omitempty"`
}

type GroupKey struct {
	Provider  string
	Mode      string
	MediaType string
	Title     string
}

func (k GroupKey) String() string {
	return fmt.Sprintf("%s:%s:%s:%s",
		normalizeKeyPart(k.Provider),
		normalizeKeyPart(k.Mode),
		normalizeKeyPart(k.MediaType),
		normalizeKeyPart(k.Title),
	)
}

type Group struct {
	Key              GroupKey
	Title            string
	ProviderName     string
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

func BuildGroups(entries []Entry) []Group {
	groupsByKey := make(map[string]*Group)
	order := make([]string, 0, len(entries))

	for _, entry := range entries {
		key := groupKeyForEntry(entry)
		keyStr := key.String()
		group, ok := groupsByKey[keyStr]
		if !ok {
			group = &Group{
				Key:          key,
				Title:        entry.Title,
				ProviderName: FirstNonEmpty(entry.ProviderName, entry.Key.Provider),
				Mode:         entry.Mode,
				MediaType:    entry.MediaType,
				LastPlayed:   entry,
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
		if group.ProviderName == "" {
			group.ProviderName = FirstNonEmpty(entry.ProviderName, entry.Key.Provider)
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

func BuildGroupLookup(entries []Entry) map[string]GroupKey {
	groups := BuildGroups(entries)
	lookup := make(map[string]GroupKey, len(groups))
	for _, group := range groups {
		lookup[group.Key.String()] = group.Key
	}
	return lookup
}

type Store struct {
	path  string
	mu    sync.Mutex
	items []Entry
}

type storageFormat struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

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

func (s *Store) All() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	res := make([]Entry, len(s.items))
	copy(res, s.items)
	return res
}

func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items = []Entry{}
	return s.save()
}

func (s *Store) save() error {
	format := storageFormat{
		Version: 1,
		Entries: s.items,
	}

	data, err := json.MarshalIndent(format, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, s.path)
}

func groupKeyForEntry(entry Entry) GroupKey {
	return GroupKey{
		Provider:  FirstNonEmpty(entry.ProviderName, entry.Key.Provider),
		Mode:      entry.Mode,
		MediaType: entry.MediaType,
		Title:     FirstNonEmpty(entry.Title, entry.Key.Title),
	}
}

func normalizeKeyPart(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

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
