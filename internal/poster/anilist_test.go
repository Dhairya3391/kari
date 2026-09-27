package poster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCleanAnilistDescription(t *testing.T) {
	in := "A great show.<br>\n<br>\n<i>Source: Crunchyroll</i>"
	out := cleanAnilistDescription(in)
	expected := "A great show. Source: Crunchyroll"
	if out != expected {
		t.Errorf("got %q, want %q", out, expected)
	}
}

func TestFetchEpisodeTitles(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": {
				"Media": {
					"streamingEpisodes": [
						{"title": "Episode 1 - The Journey's End"},
						{"title": "Episode 2: It Didn't Have to Be Magic..."},
						{"title": "Episode 3"},
						{"title": "Special"},
						{"title": "  EP 4 – Killing Magic  "}
					]
				}
			}
		}`))
	}))
	defer ts.Close()

	client := &Client{
		http:       ts.Client(),
		anilistURL: ts.URL,
	}

	titles, err := client.FetchEpisodeTitles(context.Background(), 154587)
	if err != nil {
		t.Fatalf("FetchEpisodeTitles failed: %v", err)
	}
	if titles[1] != "The Journey's End" {
		t.Errorf("titles[1] = %q, want %q", titles[1], "The Journey's End")
	}
	if titles[2] != "It Didn't Have to Be Magic..." {
		t.Errorf("titles[2] = %q, want %q", titles[2], "It Didn't Have to Be Magic...")
	}
	if _, ok := titles[3]; ok {
		t.Errorf("titles[3] = %q, want no entry (bare placeholder)", titles[3])
	}
	if titles[4] != "Killing Magic" {
		t.Errorf("titles[4] = %q, want %q", titles[4], "Killing Magic")
	}

	if _, err := client.FetchEpisodeTitles(context.Background(), 0); err == nil {
		t.Error("expected an error for anilist id 0")
	}
}

// Extra-large covers win when present: pixel-level terminals show the
// difference and caches make the bigger download once.
func TestAnilistPrefersExtraLargeCover(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"Media":{"id":21,
			"coverImage":{"extraLarge":"https://s4.anilist.co/file/xl.png","large":"https://s4.anilist.co/file/large.png","medium":"https://s4.anilist.co/file/medium.png"},
			"description":"x","genres":[],"averageScore":0}}}`))
	}))
	defer ts.Close()

	client := &Client{http: ts.Client(), anilistURL: ts.URL}
	got, err := client.anilistPosterURL(context.Background(), "One Piece")
	if err != nil {
		t.Fatalf("anilistPosterURL: %v", err)
	}
	if got != "https://s4.anilist.co/file/xl.png" {
		t.Errorf("poster = %q, want extraLarge", got)
	}
}

func TestAnilistDetailsAndPoster(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://anilist.co/" {
			t.Errorf("missing or incorrect Referer: %s", r.Header.Get("Referer"))
		}
		if r.Header.Get("Origin") != "https://anilist.co" {
			t.Errorf("missing or incorrect Origin: %s", r.Header.Get("Origin"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("missing User-Agent")
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": {
				"Media": {
					"id": 151807,
					"coverImage": {
						"large": "https://s4.anilist.co/file/large.png",
						"medium": "https://s4.anilist.co/file/medium.png"
					},
					"description": "Awesome plot synopsis.<br>Check it out.",
					"genres": ["Action", "Fantasy"],
					"averageScore": 85
				}
			}
		}`))
	}))
	defer ts.Close()

	client := &Client{
		http:       ts.Client(),
		anilistURL: ts.URL,
	}

	// Test with title
	media, err := client.fetchAnilistMedia(context.Background(), "Solo Leveling")
	if err != nil {
		t.Fatalf("fetchAnilistMedia failed: %v", err)
	}
	if media.CoverImage.Large != "https://s4.anilist.co/file/large.png" {
		t.Errorf("unexpected large cover image: %s", media.CoverImage.Large)
	}
	if media.AverageScore != 85 {
		t.Errorf("unexpected averageScore: %d", media.AverageScore)
	}

	details, err := client.anilistDetailsInfo(context.Background(), "Solo Leveling")
	if err != nil {
		t.Fatalf("anilistDetailsInfo failed: %v", err)
	}
	if details.Overview != "Awesome plot synopsis. Check it out." {
		t.Errorf("unexpected overview: %s", details.Overview)
	}
	if details.Rating != "85%" {
		t.Errorf("unexpected rating: %s", details.Rating)
	}
	if len(details.Genres) != 2 || details.Genres[0] != "Action" {
		t.Errorf("unexpected genres: %v", details.Genres)
	}

	posterURL, err := client.anilistPosterURL(context.Background(), "Solo Leveling")
	if err != nil {
		t.Fatalf("anilistPosterURL failed: %v", err)
	}
	if posterURL != "https://s4.anilist.co/file/large.png" {
		t.Errorf("unexpected posterURL: %s", posterURL)
	}
}
