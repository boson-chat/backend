package unfurl

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// Body caps. Most pages have their metadata in the <head>; YouTube emits
// its OG tags and JSON-LD ~640–700 KB into the document, and Spotify's
// embed pages carry whole track lists.
const (
	MaxHTMLBytes    = 1 << 20
	MaxYouTubeBytes = 1536 << 10
	MaxSpotifyBytes = 2 << 20
)

var youTubeHosts = map[string]bool{"youtube.com": true, "www.youtube.com": true, "m.youtube.com": true, "music.youtube.com": true, "youtu.be": true}

var htmlTypes = []string{"text/html", "application/xhtml+xml"}

// Options configure a Service. Zero values take the defaults.
type Options struct {
	Fetcher FetcherOptions
	// Entries per cache (cards, Spotify), default 2000.
	CacheSize int
	// How long a preview is kept (default 30 min) and how long "no
	// preview" is (default 1 min).
	CacheTTL, NegativeTTL time.Duration
}

// Service fetches and caches previews.
type Service struct {
	fetch       *Fetcher
	cards       *lru[Card]
	spotify     *lru[SpotifyInfo]
	cardFlight  flight[Card]
	spotFlight  flight[SpotifyInfo]
	spotifyGets func(ctx context.Context, embed string) ([]byte, error)
}

func NewService(o Options) *Service {
	if o.CacheSize <= 0 {
		o.CacheSize = 2000
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = 30 * time.Minute
	}
	if o.NegativeTTL <= 0 {
		o.NegativeTTL = time.Minute
	}
	s := &Service{
		fetch:   NewFetcher(o.Fetcher),
		cards:   newLRU[Card](o.CacheSize, o.CacheTTL, o.NegativeTTL),
		spotify: newLRU[SpotifyInfo](o.CacheSize, o.CacheTTL, o.NegativeTTL),
	}
	s.spotifyGets = func(ctx context.Context, embed string) ([]byte, error) {
		res, err := s.fetch.Get(ctx, embed, "text/html", MaxSpotifyBytes, htmlTypes...)
		if err != nil {
			return nil, err
		}
		return res.Body, nil
	}
	return s
}

// cacheKey: the URL without its fragment, which no server sees.
func cacheKey(u *url.URL) string {
	c := *u
	c.Fragment, c.RawFragment = "", ""
	return c.String()
}

// fetchCtx: a fetch shared by several askers isn't cut short by the first
// one going away; the client's timeout bounds it.
func fetchCtx(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }

// Card returns raw's preview, or nil when the page has none (not found,
// not HTML, unreachable, refused at connect time). ErrBlocked: the URL
// itself is refused. beforeFetch runs before a network fetch (not for a
// cached answer); an error from it is returned as is (rate limits).
func (s *Service) Card(ctx context.Context, raw string, beforeFetch func() error) (*Card, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || len(raw) > maxURL {
		return nil, ErrBlocked
	}
	if err := s.fetch.checkURL(u); err != nil {
		return nil, err
	}
	key := cacheKey(u)
	if c, ok := s.cards.get(key); ok {
		return c, nil
	}
	if beforeFetch != nil {
		if err := beforeFetch(); err != nil {
			return nil, err
		}
	}
	return s.cardFlight.do(key, func() *Card {
		if c, ok := s.cards.get(key); ok {
			return c
		}
		limit := int64(MaxHTMLBytes)
		if youTubeHosts[strings.ToLower(u.Hostname())] {
			limit = MaxYouTubeBytes
		}
		var card *Card
		res, err := s.fetch.Get(fetchCtx(ctx), key, "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", limit, htmlTypes...)
		if err == nil {
			c := ParseCard(string(res.Body), res.FinalURL)
			card = &c
		}
		s.cards.put(key, card)
		return card
	}), nil
}

// Spotify returns the card for a Spotify link, or nil when there's none.
// ErrBlocked: not an open.spotify.com/<type>/<id> link. beforeFetch as for
// Card.
func (s *Service) Spotify(ctx context.Context, raw string, beforeFetch func() error) (*SpotifyInfo, error) {
	if len(raw) > maxURL {
		return nil, ErrBlocked
	}
	embed, _, ok := SpotifyEmbedURL(raw)
	if !ok {
		return nil, ErrBlocked
	}
	withURL := func(i *SpotifyInfo) *SpotifyInfo {
		if i == nil {
			return nil
		}
		c := *i
		c.URL = raw
		return &c
	}
	if i, ok := s.spotify.get(embed); ok {
		return withURL(i), nil
	}
	if beforeFetch != nil {
		if err := beforeFetch(); err != nil {
			return nil, err
		}
	}
	return withURL(s.spotFlight.do(embed, func() *SpotifyInfo {
		if i, ok := s.spotify.get(embed); ok {
			return i
		}
		var info *SpotifyInfo
		if body, err := s.spotifyGets(fetchCtx(ctx), embed); err == nil {
			info = ParseSpotifyNextData(string(body), raw)
		}
		s.spotify.put(embed, info)
		return info
	})), nil
}
