package app

import (
	"testing"

	"kari/internal/player"
	"kari/internal/settings"
)

func TestSkipSettingsFrom(t *testing.T) {
	tests := []struct {
		name  string
		saved *settings.Data
		want  player.SkipSettings
	}{
		{name: "nil uses defaults", saved: nil, want: player.SkipSettings{Provider: "hybrid"}},
		{name: "empty provider keeps default", saved: &settings.Data{}, want: player.SkipSettings{Provider: "hybrid"}},
		{name: "provider override", saved: &settings.Data{SkipProvider: "skipdb"}, want: player.SkipSettings{Provider: "skipdb"}},
		{
			name: "all flags copied",
			saved: &settings.Data{
				SkipProvider:   "off",
				AutoSkipIntro:  true,
				AutoSkipEnding: true,
				SkipRecap:      true,
				SkipPreview:    true,
			},
			want: player.SkipSettings{
				Provider:       "off",
				AutoSkipIntro:  true,
				AutoSkipEnding: true,
				SkipRecap:      true,
				SkipPreview:    true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skipSettingsFrom(tt.saved); got != tt.want {
				t.Errorf("skipSettingsFrom() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
