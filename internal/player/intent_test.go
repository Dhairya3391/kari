package player

import "testing"

func TestIntentDataURI(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "absolute path with spaces and punctuation",
			in:   "/storage/emulated/0/Movies/kari/Mobile Suit Gundam/Season 01/S01E02 - Destroy Gundam!.mp4",
			want: "file:///storage/emulated/0/Movies/kari/Mobile%20Suit%20Gundam/Season%2001/S01E02%20-%20Destroy%20Gundam%21.mp4",
		},
		{
			name: "characters that would otherwise start a query or fragment",
			in:   "/sdcard/Download/Ep #1?.mkv",
			want: "file:///sdcard/Download/Ep%20%231%3F.mkv",
		},
		{
			name: "https stream is unchanged",
			in:   "https://cdn.example.com/hls/master.m3u8?token=abc",
			want: "https://cdn.example.com/hls/master.m3u8?token=abc",
		},
		{
			name: "content uri is unchanged",
			in:   "content://com.example.files/video.mp4",
			want: "content://com.example.files/video.mp4",
		},
		{
			name: "existing file uri is unchanged",
			in:   "file:///sdcard/a.mp4",
			want: "file:///sdcard/a.mp4",
		},
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := intentDataURI(tt.in); got != tt.want {
				t.Errorf("intentDataURI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
