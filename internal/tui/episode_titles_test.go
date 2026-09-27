package tui

// Behavior pins for anime episode title enrichment: providers that only
// send placeholders ("Episode 1") get real names patched in from AniList.

import (
	"testing"

	"kari/internal/provider"

	"github.com/charmbracelet/bubbles/list"
)

func episodeTitlesModel() *modelImpl {
	m := &modelImpl{
		keys:              defaultKeyMap(),
		appMode:           provider.ModeAnime,
		episodeTitlesOpID: 7,
	}
	m.episodeList = list.New(nil, list.NewDefaultDelegate(), 80, 16)
	return m
}

func TestIsPlaceholderEpisodeTitle(t *testing.T) {
	placeholders := []string{"", "  ", "Episode 1", "episode 12", "EP 3", "Ep. 5", "eps 7", "E 9"}
	for _, p := range placeholders {
		if !isPlaceholderEpisodeTitle(p) {
			t.Errorf("isPlaceholderEpisodeTitle(%q) = false, want true", p)
		}
	}
	real := []string{"The Journey's End", "Ep 1: The Journey's End", "1 - Pilot", "Special"}
	for _, r := range real {
		if isPlaceholderEpisodeTitle(r) {
			t.Errorf("isPlaceholderEpisodeTitle(%q) = true, want false", r)
		}
	}
}

// Real provider titles are never overwritten; placeholders are patched;
// episodes AniList has no name for keep theirs.
func TestOnEpisodeTitlesPatchesPlaceholders(t *testing.T) {
	m := episodeTitlesModel()
	m.episodeResults = []provider.Episode{
		{Title: "The Journey's End", Episode: 1, Season: 1},
		{Title: "Episode 2", Episode: 2, Season: 1},
		{Title: "", Episode: 3, Season: 1},
		{Title: "Episode 4", Episode: 4, Season: 1},
	}
	m.refreshEpisodeList()

	mdl, _ := m.onEpisodeTitles(episodeTitlesMsg{
		titles: map[int]string{2: "It Didn't Have to Be Magic...", 3: "Killing Magic"},
		opID:   7,
	})
	m = mdl.(*modelImpl)

	if m.episodeResults[0].Title != "The Journey's End" {
		t.Errorf("real title overwritten: %q", m.episodeResults[0].Title)
	}
	if m.episodeResults[1].Title != "It Didn't Have to Be Magic..." {
		t.Errorf("placeholder not patched: %q", m.episodeResults[1].Title)
	}
	if m.episodeResults[2].Title != "Killing Magic" {
		t.Errorf("empty title not patched: %q", m.episodeResults[2].Title)
	}
	if m.episodeResults[3].Title != "Episode 4" {
		t.Errorf("unknown episode must keep placeholder, got %q", m.episodeResults[3].Title)
	}
}

// Stale enrichment results (older navigation) are ignored.
func TestOnEpisodeTitlesIgnoresStaleOp(t *testing.T) {
	m := episodeTitlesModel()
	m.episodeTitlesOpID = 9
	m.episodeResults = []provider.Episode{{Title: "Episode 2", Episode: 2, Season: 1}}
	m.refreshEpisodeList()

	mdl, _ := m.onEpisodeTitles(episodeTitlesMsg{
		titles: map[int]string{2: "It Didn't Have to Be Magic..."},
		opID:   8,
	})
	m = mdl.(*modelImpl)
	if m.episodeResults[0].Title != "Episode 2" {
		t.Errorf("stale titles must be ignored, got %q", m.episodeResults[0].Title)
	}
}
