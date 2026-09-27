package tui

import (
	"github.com/charmbracelet/bubbles/key"
)

type keyMap struct {
	Move              key.Binding
	Filter            key.Binding
	Search            key.Binding
	Type              key.Binding
	Audio             key.Binding
	Select            key.Binding
	Play              key.Binding
	PlayNext          key.Binding
	Player            key.Binding
	Download          key.Binding
	Autoplay          key.Binding
	Help              key.Binding
	Back              key.Binding
	Home              key.Binding
	Quit              key.Binding
	Stop              key.Binding
	Restart           key.Binding
	History           key.Binding
	Settings          key.Binding
	Delete            key.Binding
	ClearHistory      key.Binding
	ToggleSelect      key.Binding
	SelectAll         key.Binding
	DeselectAll       key.Binding
	BatchDownload     key.Binding
	Top               key.Binding
	Bottom            key.Binding
	ReaderNext        key.Binding
	ReaderPrev        key.Binding
	ReaderNextChapter key.Binding
	ReaderPrevChapter key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Move: key.NewBinding(
			key.WithKeys("up", "down", "j", "k"),
			key.WithHelp("↑/↓", "move"),
		),
		Filter: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "filter"),
		),
		Search: key.NewBinding(
			key.WithKeys(" ", "space"),
			key.WithHelp("space", "search"),
		),
		Type: key.NewBinding(
			key.WithKeys("tab", "shift+tab"),
			key.WithHelp("tab", "switch mode"),
		),
		Audio: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "sub/dub"),
		),
		Select: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "action"),
		),
		Play: key.NewBinding(
			key.WithKeys("enter", "p"),
			key.WithHelp("enter", "play"),
		),
		PlayNext: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "play next"),
		),
		Player: key.NewBinding(
			key.WithKeys("ctrl+p"),
			key.WithHelp("ctrl+p", "player"),
		),
		Download: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "download"),
		),
		Autoplay: key.NewBinding(
			key.WithKeys("A"),
			key.WithHelp("A", "autoplay"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Back: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "back"),
		),
		Home: key.NewBinding(
			key.WithKeys("ctrl+h"),
			key.WithHelp("ctrl+h", "home"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
		Stop: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "stop"),
		),
		Restart: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "restart"),
		),
		History: key.NewBinding(
			key.WithKeys("h", "H"),
			key.WithHelp("h", "history"),
		),
		Settings: key.NewBinding(
			key.WithKeys("s", "S"),
			key.WithHelp("s", "settings"),
		),
		Delete: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete"),
		),
		ClearHistory: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "clear all"),
		),
		ToggleSelect: key.NewBinding(
			key.WithKeys(" ", "space"),
			key.WithHelp("space", "toggle"),
		),
		SelectAll: key.NewBinding(
			key.WithKeys("ctrl+a"),
			key.WithHelp("ctrl+a", "select all"),
		),
		DeselectAll: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "deselect"),
		),
		BatchDownload: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "batch dl"),
		),
		Top: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G"),
			key.WithHelp("G", "bottom"),
		),
		ReaderNext: key.NewBinding(
			key.WithKeys("right", "l", "pgdown"),
			key.WithHelp("→", "next page"),
		),
		ReaderPrev: key.NewBinding(
			key.WithKeys("left", "h", "pgup"),
			key.WithHelp("←", "prev page"),
		),
		ReaderNextChapter: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "next chapter"),
		),
		ReaderPrevChapter: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "prev chapter"),
		),
	}
}
