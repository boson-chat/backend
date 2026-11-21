package unfurl

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Card is a link preview: OpenGraph / title metadata from a page. The same
// JSON as the desktop shell's OgCard.
type Card struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Image       string `json:"image,omitempty"`
	SiteName    string `json:"siteName,omitempty"`
	Author      string `json:"author,omitempty"`
	Date        string `json:"date,omitempty"` // ISO 8601, when the page has one
}

// Field caps: longer values are cut (no card shows more).
const (
	maxTitle = 300
	maxDesc  = 1000
	maxShort = 200
	maxURL   = 2048
)

func metaRe(attr, key string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)<meta\b[^>]*\b` + attr + `\s*=\s*["']` + regexp.QuoteMeta(key) + `["'][^>]*>`)
}

var metaKeys = func() map[string]*regexp.Regexp {
	m := map[string]*regexp.Regexp{}
	for _, k := range []string{"og:title", "og:description", "description", "og:image", "twitter:image", "og:site_name",
		"author", "article:author", "og:article:author", "article:published_time", "og:article:published_time"} {
		m[k] = metaRe(`(?:property|name)`, k)
	}
	return m
}()

var (
	contentRe        = regexp.MustCompile(`(?i)\bcontent\s*=\s*["']([^"']*)["']`)
	titleTagRe       = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	authorStrRe      = regexp.MustCompile(`(?i)"author"\s*:\s*"([^"\\]{1,160})"`)
	authorObjRe      = regexp.MustCompile(`(?i)"author"\s*:\s*\{[^}]{0,400}?"name"\s*:\s*"([^"\\]{1,160})"`)
	authorLnkRe      = regexp.MustCompile(`(?i)<link\b[^>]*\bitemprop\s*=\s*["']name["'][^>]*\bcontent\s*=\s*["']([^"']{1,160})["'][^>]*>`)
	datePublishedRe  = metaRe("itemprop", "datePublished")
	uploadDateRe     = regexp.MustCompile(`(?i)"uploadDate"\s*:\s*"([^"\\]{1,300})"`)
	datePublishedJSR = regexp.MustCompile(`(?i)"datePublished"\s*:\s*"([^"\\]{1,300})"`)
)

func tagContent(doc string, re *regexp.Regexp) string {
	tag := re.FindString(doc)
	if tag == "" {
		return ""
	}
	m := contentRe.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return html.UnescapeString(strings.TrimSpace(m[1]))
}

func metaContent(doc, key string) string { return tagContent(doc, metaKeys[key]) }

func jsonStr(doc string, re *regexp.Regexp) string {
	m := re.FindStringSubmatch(doc)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// clip cuts s to n runes (and drops invalid UTF-8).
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

// ParseCard reads a page's preview metadata (desktop/unfurl.go parseOg).
func ParseCard(doc, pageURL string) Card {
	// Some sites (YouTube) put metadata far down the document.
	if len(doc) > 1_600_000 {
		doc = doc[:1_600_000]
	}
	card := Card{URL: pageURL}
	title := ""
	if m := titleTagRe.FindStringSubmatch(doc); m != nil {
		title = html.UnescapeString(strings.TrimSpace(m[1]))
	}
	card.Title = clip(first(metaContent(doc, "og:title"), title), maxTitle)
	card.Description = clip(first(metaContent(doc, "og:description"), metaContent(doc, "description")), maxDesc)
	if img := absolutize(first(metaContent(doc, "og:image"), metaContent(doc, "twitter:image")), pageURL); len(img) <= maxURL {
		card.Image = img
	}
	card.SiteName = clip(metaContent(doc, "og:site_name"), maxShort)
	author := ""
	if m := authorStrRe.FindStringSubmatch(doc); m != nil {
		author = m[1]
	} else if m := authorObjRe.FindStringSubmatch(doc); m != nil {
		author = m[1]
	} else if m := authorLnkRe.FindStringSubmatch(doc); m != nil {
		author = m[1]
	}
	card.Author = clip(first(metaContent(doc, "author"), metaContent(doc, "article:author"), metaContent(doc, "og:article:author"), html.UnescapeString(author)), maxShort)
	card.Date = clip(first(metaContent(doc, "article:published_time"), metaContent(doc, "og:article:published_time"),
		tagContent(doc, datePublishedRe), jsonStr(doc, uploadDateRe), jsonStr(doc, datePublishedJSR)), 64)
	return card
}

// absolutize resolves a possibly relative image URL against the page; only
// http(s) results are kept.
func absolutize(img, base string) string {
	if img == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u, err := b.Parse(img)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}
