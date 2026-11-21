package unfurl

import (
	"encoding/json"
	"strings"
	"testing"
)

// Ported from boson desktop/unfurl_test.go: the same inputs give the same
// cards as the desktop fetcher.
func TestParseCard(t *testing.T) {
	doc := `<head>
      <meta property="og:title" content="Hello &amp; World">
      <meta content="A nice post" name="og:description">
      <meta property="og:image" content="https://cdn.example.com/x.png">
      <meta property="og:site_name" content="Example">
    </head>`
	got := ParseCard(doc, "https://example.com/p")
	want := Card{URL: "https://example.com/p", Title: "Hello & World", Description: "A nice post", Image: "https://cdn.example.com/x.png", SiteName: "Example"}
	if got != want {
		t.Fatalf("og tags: got %+v", got)
	}

	c := ParseCard(`<head><title>Just a Title</title><meta name="description" content="desc"></head>`, "https://x.com")
	if c.Title != "Just a Title" || c.Description != "desc" || c.Image != "" {
		t.Fatalf("fallbacks: %+v", c)
	}

	if img := ParseCard(`<meta property="og:image" content="/img/cover.jpg">`, "https://example.com/blog/post").Image; img != "https://example.com/img/cover.jpg" {
		t.Fatalf("relative image: %q", img)
	}
	if img := ParseCard(`<meta name="twitter:image" content="https://t.example/i.png">`, "https://example.com").Image; img != "https://t.example/i.png" {
		t.Fatalf("twitter image: %q", img)
	}

	m := ParseCard(`<meta name="author" content="Jane Doe">
      <meta property="article:published_time" content="2025-02-20T10:00:00Z">`, "https://blog.example.com/p")
	if m.Author != "Jane Doe" || m.Date != "2025-02-20T10:00:00Z" {
		t.Fatalf("meta author/date: %+v", m)
	}

	yt := strings.Repeat("x", 5000) + `<script type="application/ld+json">
      {"@type":"VideoObject","author":{"@type":"Person","name":"abe's projects"},"uploadDate":"2025-02-20T10:00:44-08:00"}
      </script><meta property="og:title" content="I built my ideal mini computer">`
	y := ParseCard(yt, "https://www.youtube.com/watch?v=x")
	if y.Title != "I built my ideal mini computer" || y.Author != "abe's projects" || y.Date != "2025-02-20T10:00:44-08:00" {
		t.Fatalf("json-ld: %+v", y)
	}

	// YouTube-style: the tags ~700 KB in still parse.
	far := ParseCard(strings.Repeat("y", 700_000)+`<meta property="og:title" content="far down">`, "https://www.youtube.com/watch?v=y")
	if far.Title != "far down" {
		t.Fatalf("far: %+v", far)
	}
}

func TestParseCardClipsFields(t *testing.T) {
	long := strings.Repeat("é", 2000)
	c := ParseCard(`<meta property="og:title" content="`+long+`"><meta property="og:description" content="`+long+`"><meta property="og:site_name" content="`+long+`">`, "https://e.com")
	if n := len([]rune(c.Title)); n != maxTitle+1 {
		t.Fatalf("title runes %d", n)
	}
	if n := len([]rune(c.Description)); n != maxDesc+1 {
		t.Fatalf("desc runes %d", n)
	}
	if n := len([]rune(c.SiteName)); n != maxShort+1 {
		t.Fatalf("site runes %d", n)
	}
	if img := ParseCard(`<meta property="og:image" content="https://e.com/`+strings.Repeat("a", 3000)+`">`, "https://e.com").Image; img != "" {
		t.Fatal("over-long image URL kept")
	}
}

func TestAbsolutize(t *testing.T) {
	cases := map[[2]string]string{
		{"https://a.com/x.png", "https://b.com"}:      "https://a.com/x.png",
		{"/x.png", "https://b.com/p"}:                 "https://b.com/x.png",
		{"//cdn.b.com/x.png", "https://b.com/p"}:      "https://cdn.b.com/x.png",
		{"javascript:alert(1)", "https://b.com"}:      "",
		{"data:image/png;base64,AA", "https://b.com"}: "",
		{"", "https://b.com"}:                         "",
	}
	for in, want := range cases {
		if got := absolutize(in[0], in[1]); got != want {
			t.Errorf("absolutize(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestSpotifyEmbedURL(t *testing.T) {
	if e, k, ok := SpotifyEmbedURL("https://open.spotify.com/track/abc123"); !ok || e != "https://open.spotify.com/embed/track/abc123" || k != "track" {
		t.Fatalf("track: %s %s %v", e, k, ok)
	}
	if e, _, _ := SpotifyEmbedURL("https://open.spotify.com/intl-de/album/xyz"); e != "https://open.spotify.com/embed/album/xyz" {
		t.Fatalf("intl: %s", e)
	}
	if _, k, _ := SpotifyEmbedURL("https://open.spotify.com/embed/playlist/pl1"); k != "playlist" {
		t.Fatalf("embed path: %s", k)
	}
	for _, bad := range []string{
		"https://open.spotify.com/user/someone", "https://evil.example.com/track/abc", "not a url",
		"http://127.0.0.1:9/track/x", "https://open.spotify.com:8443/track/x", "ftp://open.spotify.com/track/x",
	} {
		if _, _, ok := SpotifyEmbedURL(bad); ok {
			t.Errorf("%s accepted", bad)
		}
	}
}

func spotifyPage(entity any) string {
	b, _ := json.Marshal(map[string]any{"props": map[string]any{"pageProps": map[string]any{"state": map[string]any{"data": map[string]any{"entity": entity}}}}})
	return `<html><script id="__NEXT_DATA__" type="application/json">` + string(b) + `</script></html>`
}

func TestParseSpotifyNextData(t *testing.T) {
	track := ParseSpotifyNextData(spotifyPage(map[string]any{
		"type": "track", "title": "Speak To Me", "artists": []any{map[string]any{"name": "Pink Floyd"}}, "duration": 234000,
		"audioPreview":   map[string]any{"url": "https://p.scdn.co/mp3-preview/abc"},
		"visualIdentity": map[string]any{"image": []any{map[string]any{"url": "https://img/64", "maxWidth": 64}, map[string]any{"url": "https://img/640", "maxWidth": 640}}},
	}), "https://open.spotify.com/track/x")
	if track == nil || track.Title != "Speak To Me" || track.Subtitle != "Pink Floyd" || track.Cover != "https://img/640" || track.PreviewURL != "https://p.scdn.co/mp3-preview/abc" || track.DurationMs != 234000 {
		t.Fatalf("track: %+v", track)
	}
	pl := ParseSpotifyNextData(spotifyPage(map[string]any{
		"type": "playlist", "title": "Today’s Top Hits", "subtitle": "Spotify",
		"coverArt": map[string]any{"sources": []any{map[string]any{"url": "https://c/300", "width": 300}, map[string]any{"url": "https://c/64", "width": 64}}},
		"trackList": []any{
			map[string]any{"title": "A", "subtitle": "Artist A", "duration": 200000, "audioPreview": map[string]any{"url": "https://p/a"}},
			map[string]any{"title": "B", "subtitle": "Artist B", "duration": 180000},
			map[string]any{"title": "", "subtitle": "skip me"},
			map[string]any{"title": "C", "audioPreview": map[string]any{"url": "javascript:alert(1)"}},
		},
	}), "https://open.spotify.com/playlist/x")
	if pl == nil || pl.Cover != "https://c/300" || pl.Subtitle != "Spotify" || len(pl.Tracks) != 3 ||
		pl.Tracks[0] != (SpotifyTrack{Title: "A", Artist: "Artist A", DurationMs: 200000, PreviewURL: "https://p/a"}) || pl.Tracks[1].PreviewURL != "" || pl.Tracks[2].PreviewURL != "" {
		t.Fatalf("playlist: %+v", pl)
	}
	if ParseSpotifyNextData("<html>nope</html>", "u") != nil || ParseSpotifyNextData(spotifyPage(nil), "u") != nil ||
		ParseSpotifyNextData(spotifyPage(map[string]any{"type": "track"}), "u") != nil {
		t.Fatal("expected nil without data")
	}
}
