package unfurl

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// SpotifyTrack and SpotifyInfo: the same JSON as the desktop shell's
// SpotifyService (desktop/spotify.go).
type SpotifyTrack struct {
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	DurationMs int    `json:"durationMs"`
	PreviewURL string `json:"previewUrl,omitempty"`
}

type SpotifyInfo struct {
	URL        string         `json:"url"`
	Type       string         `json:"type"`
	Title      string         `json:"title"`
	Subtitle   string         `json:"subtitle,omitempty"`
	Cover      string         `json:"cover,omitempty"`
	DurationMs int            `json:"durationMs,omitempty"`
	PreviewURL string         `json:"previewUrl,omitempty"`
	Tracks     []SpotifyTrack `json:"tracks,omitempty"`
}

var (
	spotifyTypes  = map[string]bool{"track": true, "album": true, "playlist": true, "artist": true, "episode": true, "show": true}
	spotifyPathRe = regexp.MustCompile(`^/(?:intl-[a-z]{2}/)?(?:embed/)?([a-z]+)/([A-Za-z0-9]+)`)
	nextDataRe    = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)
)

// SpotifyEmbedURL maps a Spotify link to its embed page — the only page
// the Spotify fetch ever asks for (its own SSRF guard).
func SpotifyEmbedURL(raw string) (embed, kind string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host != "open.spotify.com" && host != "play.spotify.com" {
		return "", "", false
	}
	if u.Port() != "" && u.Port() != "443" && u.Port() != "80" {
		return "", "", false
	}
	m := spotifyPathRe.FindStringSubmatch(u.Path)
	if m == nil || !spotifyTypes[m[1]] {
		return "", "", false
	}
	return "https://open.spotify.com/embed/" + m[1] + "/" + m[2], m[1], true
}

type spotifyImage struct {
	URL      string `json:"url"`
	Width    int    `json:"width"`
	MaxWidth int    `json:"maxWidth"`
}

type spotifyEntity struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Name     string `json:"name"`
	Subtitle string `json:"subtitle"`
	Artists  []struct {
		Name string `json:"name"`
	} `json:"artists"`
	CoverArt *struct {
		Sources []spotifyImage `json:"sources"`
	} `json:"coverArt"`
	VisualIdentity *struct {
		Image []spotifyImage `json:"image"`
	} `json:"visualIdentity"`
	Duration     *float64 `json:"duration"`
	AudioPreview *struct {
		URL string `json:"url"`
	} `json:"audioPreview"`
	TrackList []struct {
		Title        string   `json:"title"`
		Subtitle     string   `json:"subtitle"`
		Duration     *float64 `json:"duration"`
		AudioPreview *struct {
			URL string `json:"url"`
		} `json:"audioPreview"`
	} `json:"trackList"`
}

// httpsURL keeps only https URLs (covers, previews), cut to a sane length.
func httpsURL(s string) string {
	if len(s) > maxURL || !strings.HasPrefix(s, "https://") {
		return ""
	}
	return s
}

// ParseSpotifyNextData reads the entity from an embed page's __NEXT_DATA__.
func ParseSpotifyNextData(doc, pageURL string) *SpotifyInfo {
	m := nextDataRe.FindStringSubmatch(doc)
	if m == nil {
		return nil
	}
	var data struct {
		Props struct {
			PageProps struct {
				State struct {
					Data struct {
						Entity *spotifyEntity `json:"entity"`
					} `json:"data"`
				} `json:"state"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if json.Unmarshal([]byte(m[1]), &data) != nil {
		return nil
	}
	e := data.Props.PageProps.State.Data.Entity
	if e == nil || e.Type == "" {
		return nil
	}
	title := first(e.Title, e.Name)
	if title == "" {
		return nil
	}
	info := &SpotifyInfo{URL: pageURL, Type: clip(e.Type, 32), Title: clip(title, maxTitle)}
	var names []string
	for _, a := range e.Artists {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	if len(e.Artists) > 0 {
		info.Subtitle = clip(strings.Join(names, ", "), maxShort)
	} else {
		info.Subtitle = clip(e.Subtitle, maxShort)
	}
	info.Cover = httpsURL(pickCover(e))
	if e.Duration != nil {
		info.DurationMs = int(*e.Duration)
	}
	if e.AudioPreview != nil {
		info.PreviewURL = httpsURL(e.AudioPreview.URL)
	}
	for i, t := range e.TrackList {
		if i >= 100 {
			break
		}
		if t.Title == "" {
			continue
		}
		tr := SpotifyTrack{Title: clip(t.Title, maxTitle), Artist: clip(t.Subtitle, maxShort)}
		if t.Duration != nil {
			tr.DurationMs = int(*t.Duration)
		}
		if t.AudioPreview != nil {
			tr.PreviewURL = httpsURL(t.AudioPreview.URL)
		}
		info.Tracks = append(info.Tracks, tr)
	}
	return info
}

// pickCover takes the widest cover art, else the widest visual-identity image.
func pickCover(e *spotifyEntity) string {
	widest := func(imgs []spotifyImage, w func(spotifyImage) int) string {
		if len(imgs) == 0 {
			return ""
		}
		s := append([]spotifyImage(nil), imgs...)
		sort.SliceStable(s, func(i, j int) bool { return w(s[i]) > w(s[j]) })
		return s[0].URL
	}
	if e.CoverArt != nil {
		if u := widest(e.CoverArt.Sources, func(i spotifyImage) int { return i.Width }); u != "" {
			return u
		}
	}
	if e.VisualIdentity != nil {
		return widest(e.VisualIdentity.Image, func(i spotifyImage) int { return i.MaxWidth })
	}
	return ""
}
