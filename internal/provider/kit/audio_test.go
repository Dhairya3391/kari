package kit

import (
	"reflect"
	"testing"
)

func TestAudioLabel(t *testing.T) {
	cases := []struct {
		name  string
		langs []string
		want  string
	}{
		{name: "single", langs: []string{"Hindi"}, want: "Hindi"},
		{name: "multiple", langs: []string{"en", "hi", "ta"}, want: "English · Hindi · Tamil"},
		{name: "deduplicated", langs: []string{"Hindi", "hi"}, want: "Hindi"},
		{name: "empty", langs: nil, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AudioLabel(tc.langs); got != tc.want {
				t.Errorf("AudioLabel(%v) = %q, want %q", tc.langs, got, tc.want)
			}
		})
	}
}

func TestSplitAudioLabel(t *testing.T) {
	cases := []struct {
		name  string
		label string
		want  []string
	}{
		{name: "canonical delimiter", label: "English · Hindi · Tamil", want: []string{"English", "Hindi", "Tamil"}},
		{name: "bare delimiter", label: "Hindi·English", want: []string{"Hindi", "English"}},
		{name: "trims empties", label: " Hindi ·  · English ", want: []string{"Hindi", "English"}},
		{name: "single", label: "English", want: []string{"English"}},
		{name: "empty", label: "", want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitAudioLabel(tc.label)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitAudioLabel(%q) = %#v, want %#v", tc.label, got, tc.want)
			}
		})
	}
}

func TestAudioLabelRoundTrip(t *testing.T) {
	got := SplitAudioLabel(AudioLabel([]string{"en", "hi", "ta"}))
	want := []string{"English", "Hindi", "Tamil"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %#v, want %#v", got, want)
	}
}

func TestSubtitleLanguage(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"English", "en"},
		{"en", "en"},
		{"Japanese", "ja"},
		{"English SDH", "en"},
	}
	for _, tc := range cases {
		if got := SubtitleLanguage(tc.raw); got != tc.want {
			t.Errorf("SubtitleLanguage(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
