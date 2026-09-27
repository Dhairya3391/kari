package selfupdate

import (
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1   string
		v2   string
		want int
	}{
		// Equal versions
		{"v5.0.0", "v5.0.0", 0},
		{"v5.0.0", "5.0.0", 0},
		{"5.0.0", "v5.0.0", 0},
		{"v5.0.0-dirty", "5.0.0", 0},
		{"V5.0.0", "5.0.0", 0},
		{"v5.0.0+build123", "5.0.0", 0},

		// Major version difference
		{"v4.9.9", "v5.0.0", -1},
		{"v5.0.0", "v4.9.9", 1},
		{"v5.0.0", "v6.0.0", -1},

		// Minor version difference
		{"v5.0.0", "v5.1.0", -1},
		{"v5.2.0", "v5.1.0", 1},

		// Patch version difference
		{"v5.0.0", "v5.0.1", -1},
		{"v5.0.2", "v5.0.1", 1},

		// Multi-digit numeric precedence (100 > 99)
		{"v1.0.99", "v1.0.100", -1},
		{"v1.0.100", "v1.0.99", 1},

		// Prerelease versions
		{"v5.0.0-rc.1", "v5.0.0", -1},
		{"v5.0.0", "v5.0.0-rc.1", 1},
		{"v5.0.0-alpha.1", "v5.0.0-alpha.2", -1},
		{"v5.0.0-beta.1", "v5.0.0-alpha.1", 1},
		{"v5.0.0-rc.1", "v5.0.0-rc.1", 0},

		// Dev versions
		{"0.0.0-dev", "v5.0.0", -1},
		{"dev", "v5.0.0", -1},
		{"v5.0.0", "dev", 1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.v1, tt.v2)
		if got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
		}
	}
}

func TestCommitsMatch(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want bool
	}{
		{"7309c71", "7309c710a48cd75df4f60a322ba8d91263e7cafd", true},
		{"7309c710a48cd75df4f60a322ba8d91263e7cafd", "7309c71", true},
		{"7309c71", "7309C71", true},
		{"cfe04e3", "7309c710a48cd75df4f60a322ba8d91263e7cafd", false},
		{"dev", "7309c71", false},
		{"dev", "dev", true},
		{"", "7309c71", false},
		{"7309c71", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		got := CommitsMatch(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CommitsMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestShouldUpdate(t *testing.T) {
	tests := []struct {
		name           string
		currentVersion string
		currentCommit  string
		latestVersion  string
		latestCommit   string
		want           bool
	}{
		{
			name:           "same version same commit - already up to date",
			currentVersion: "v5.0.0",
			currentCommit:  "7309c71",
			latestVersion:  "v5.0.0",
			latestCommit:   "7309c710a48cd75df4f60a322ba8d91263e7cafd",
			want:           false,
		},
		{
			name:           "same version different commit - needs update",
			currentVersion: "v5.0.0",
			currentCommit:  "cfe04e3",
			latestVersion:  "v5.0.0",
			latestCommit:   "7309c710a48cd75df4f60a322ba8d91263e7cafd",
			want:           true,
		},
		{
			name:           "upstream version newer - needs update",
			currentVersion: "v5.0.0",
			currentCommit:  "7309c71",
			latestVersion:  "v5.0.1",
			latestCommit:   "abcdef1234567890abcdef1234567890abcdef12",
			want:           true,
		},
		{
			name:           "local version ahead of upstream - already up to date",
			currentVersion: "v5.1.0",
			currentCommit:  "7309c71",
			latestVersion:  "v5.0.0",
			latestCommit:   "7309c710a48cd75df4f60a322ba8d91263e7cafd",
			want:           false,
		},
		{
			name:           "dev build to release - needs update",
			currentVersion: "0.0.0-dev",
			currentCommit:  "dev",
			latestVersion:  "v5.0.0",
			latestCommit:   "7309c710a48cd75df4f60a322ba8d91263e7cafd",
			want:           true,
		},
		{
			name:           "same version missing latest commit - do not update",
			currentVersion: "v5.0.0",
			currentCommit:  "7309c71",
			latestVersion:  "v5.0.0",
			latestCommit:   "",
			want:           false,
		},
	}

	for _, tt := range tests {
		got := ShouldUpdate(tt.currentVersion, tt.currentCommit, tt.latestVersion, tt.latestCommit)
		if got != tt.want {
			t.Errorf("ShouldUpdate(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestShortSHA(t *testing.T) {
	if got := ShortSHA("7309c710a48cd75df4f60a322ba8d91263e7cafd"); got != "7309c71" {
		t.Errorf("ShortSHA full = %q, want %q", got, "7309c71")
	}
	if got := ShortSHA("7309c71"); got != "7309c71" {
		t.Errorf("ShortSHA short = %q, want %q", got, "7309c71")
	}
	if got := ShortSHA("dev"); got != "dev" {
		t.Errorf("ShortSHA dev = %q, want %q", got, "dev")
	}
}

func TestReleaseCommitSHAFromTargetCommitish(t *testing.T) {
	r := &Release{
		TagName:         "v5.0.0",
		TargetCommitish: "7309c710a48cd75df4f60a322ba8d91263e7cafd",
	}
	sha, err := r.CommitSHA()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "7309c710a48cd75df4f60a322ba8d91263e7cafd" {
		t.Errorf("sha = %q, want 7309c710a48cd75df4f60a322ba8d91263e7cafd", sha)
	}
}
