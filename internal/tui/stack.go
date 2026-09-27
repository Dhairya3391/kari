package tui

import (
	"strings"
)

// Screen identifies one view in the application.
type Screen string

const (
	ScreenSearch    Screen = "search"
	ScreenResults   Screen = "results"
	ScreenEpisodes  Screen = "episodes"
	ScreenChapters  Screen = "chapters"
	ScreenReader    Screen = "reader"
	ScreenPreview   Screen = "preview"
	ScreenHistory   Screen = "history"
	ScreenSettings  Screen = "settings"
	ScreenDownloads Screen = "downloads"
)

// StackItem stores the metadata of a single screen on the navigation stack.
type StackItem struct {
	Screen Screen
	// Crumb is the custom breadcrumb segment for this screen (e.g. "anime", "the boys", "s01e01").
	Crumb string
}

// Stack manages a history stack of visited screens to support breadcrumbs and Esc-to-back navigation.
type Stack struct {
	items []StackItem
}

// NewStack initializes a navigation stack rooted at the Search screen.
// The initialCrumb parameter is reserved for future use and currently unused.
func NewStack(_ string) *Stack {
	return &Stack{
		items: []StackItem{
			{Screen: ScreenSearch, Crumb: "search"},
		},
	}
}

// Push appends a new screen to the navigation stack.
func (s *Stack) Push(screen Screen, crumb string) {
	if crumb == "" {
		crumb = string(screen)
	}
	s.items = append(s.items, StackItem{Screen: screen, Crumb: crumb})
}

// Pop removes the top screen from the navigation stack, returning the popped screen.
// Does not pop the root screen.
func (s *Stack) Pop() (StackItem, bool) {
	if len(s.items) <= 1 {
		return StackItem{}, false
	}
	top := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return top, true
}

// Replace substitutes the top screen on the stack.
func (s *Stack) Replace(screen Screen, crumb string) {
	if crumb == "" {
		crumb = string(screen)
	}
	if len(s.items) == 0 {
		s.items = []StackItem{{Screen: screen, Crumb: crumb}}
		return
	}
	s.items[len(s.items)-1] = StackItem{Screen: screen, Crumb: crumb}
}

// Current returns the active top screen.
func (s *Stack) Current() StackItem {
	if len(s.items) == 0 {
		return StackItem{Screen: ScreenSearch, Crumb: "search"}
	}
	return s.items[len(s.items)-1]
}

// Depth returns the number of screens on the stack.
func (s *Stack) Depth() int {
	return len(s.items)
}

// Breadcrumbs generates the breadcrumb path components (e.g. ["kari", "tv", "the boys", "s01e01"]).
func (s *Stack) Breadcrumbs() []string {
	crumbs := []string{"kari"}
	for _, item := range s.items {
		c := strings.TrimSpace(item.Crumb)
		if c != "" && c != "kari" {
			crumbs = append(crumbs, strings.ToLower(c))
		}
	}
	return crumbs
}
