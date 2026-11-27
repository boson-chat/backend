package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jinzhu/configor"
)

// Config aggregates every subsystem's tunables. Loaded via
// configor — values come from (in order of precedence):
//  1. Environment variables (matching the `env:"NAME"` tag).
//  2. The local JSON config file (path resolved by
//     configFileSearchPaths below; tolerates missing).
//  3. The `default:"..."` tag.
//
// This matches the convention used across other weeb-vip Go services
// (anime-api, gateway-proxy, etc.) — a versioned `config.dev.json`
// in-repo carries the local defaults so `make run` works out of
// the box, and prod overrides via env vars set by the k8s manifest.
type Config struct {
	AppConfig      AppConfig
	DBConfig       DBConfig
	AuthConfig     AuthConfig
	InternalConfig InternalConfig
	StackConfig    StackConfig
	UnfurlConfig   UnfurlConfig
}

// UnfurlConfig is GET /v1/unfurl and /v1/unfurl/spotify: link previews
// for clients that can't fetch other sites themselves (the web client).
// Anyone may call them: anonymous callers are limited per address, callers
// with a valid access token per user. Cached answers don't count.
type UnfurlConfig struct {
	Enabled bool `env:"UNFURL_ENABLED" default:"true"`
	// Fetches a minute per client address (IPv6: per /64) for anonymous
	// callers. 0 = unlimited.
	RatePerIP int `env:"UNFURL_RATE_PER_IP" default:"120"`
	// Fetches a minute per signed-in user. 0 = unlimited.
	RatePerUser int `env:"UNFURL_RATE_PER_USER" default:"60"`
	// Entries per cache (cards, Spotify) and how long they're kept.
	CacheSize       int `env:"UNFURL_CACHE_SIZE" default:"2000"`
	CacheTTLSeconds int `env:"UNFURL_CACHE_TTL_SECONDS" default:"1800"`
	// Proxies whose X-Forwarded-For is believed (comma-separated CIDRs).
	// Empty: loopback and private addresses (the compose network).
	TrustedProxies string `env:"TRUSTED_PROXIES" default:""`
	// End-to-end tests only, ignored when ENV=production: name=host[:port]
	// pairs (comma-separated) fetched over plain http from that address
	// without the SSRF guard, e.g. a test page under a public-looking name.
	TestHostMap string `env:"UNFURL_TEST_HOST_MAP" default:""`
}

// InternalConfig is the internal listener: GET /internal/config/status and
// the admin panel's /internal/nick-claims/* endpoints. It is meant for the
// compose network only; never publish its port.
type InternalConfig struct {
	// Port of the internal listener (all interfaces). Default 3001.
	Port int `env:"INTERNAL_PORT" default:"3001"`
	// Full listen address; wins over INTERNAL_PORT when set (e.g.
	// 127.0.0.1:13001). "off" turns the listener off.
	Addr string `env:"INTERNAL_ADDR"`
	// Shared secret the admin panel sends as X-Internal-Token (or
	// Authorization: Bearer) on /internal/nick-claims/*. Empty: those
	// endpoints answer 503 internal_api_disabled (/internal/config/status
	// needs no token). INTERNAL_API_TOKEN is accepted as an older name.
	Token    string `env:"INTERNAL_TOKEN"`
	APIToken string `env:"INTERNAL_API_TOKEN"`
}

// ListenAddr is the internal listener's address ("" = off).
func (c InternalConfig) ListenAddr() string {
	switch {
	case c.Addr == "off":
		return ""
	case c.Addr != "":
		return c.Addr
	case c.Port > 0:
		return fmt.Sprintf(":%d", c.Port)
	}
	return ""
}

// SharedToken is INTERNAL_TOKEN, else INTERNAL_API_TOKEN.
func (c InternalConfig) SharedToken() string {
	if c.Token != "" {
		return c.Token
	}
	return c.APIToken
}

// StackConfig is the stack's shared config file (config/boson.yaml).
type StackConfig struct {
	// Path of the file, e.g. /config/boson.yaml. Empty: env values only.
	Path string `env:"BOSON_CONFIG"`
}

type AppConfig struct {
	AppName string `default:"boson"`
	Port    int    `env:"PORT" default:"3000"`
	Env     string `env:"ENV" default:"development"`
	// Pages allowed to call the API from a browser (CORS): comma-separated
	// origins, and/or "directory" = the origins at OriginsURL (other Boson
	// installs' web clients, whose federated sessions call their home API
	// here with its bearer token). Fixed origins stay allowed when that
	// list can't be fetched. No wildcard (never "*"). Don't give any field
	// here a default:"*" tag: configor reads tag defaults as YAML, where *
	// is an alias, and then silently drops every default.
	AllowedOrigins string `env:"ALLOWED_ORIGINS" default:"http://localhost:5173"`
	// The web origins list for "directory": the auth service's
	// GET /v1/web-origins ({"origins": [...]}), e.g.
	// http://auth:8080/v1/web-origins. Fetched at start and every
	// OriginsRefresh (unset or <= 0: 5m); the last good list stays when it
	// fails.
	OriginsURL     string        `env:"ORIGINS_URL"`
	OriginsRefresh time.Duration `env:"ORIGINS_REFRESH"`
	// NickClaim subsystem: automatic NickServ registration of a user's
	// nick on other IRC networks. The backend mints
	// `reg+<userid>-<short>@<email domain>` addresses and a POP3 worker
	// drains the catch-all mailbox they land in. Off unless
	// NICK_CLAIM_ENABLED=true or `nick_claim.enabled: true` in the stack
	// config file. The effective settings (file, then env, then defaults)
	// are resolved by NickClaimFromEnv / LoadNickClaimFile in nickclaim.go.
	NickClaimEnabled          bool   `env:"NICK_CLAIM_ENABLED" default:"false"`
	NickClaimEmailDomain      string `env:"NICK_CLAIM_EMAIL_DOMAIN" default:""`
	NickClaimRateLimitPerHour int    `env:"NICK_CLAIM_RATE_LIMIT_PER_HOUR" default:"5"`

	// Inbound mailbox (POP3). NICK_CLAIM_POP3_* win; the older
	// PURELYMAIL_POP3_* below are still read as fallbacks. Empty / 0
	// means unset (NickClaimPOP3TLS is "true"/"false"/"").
	NickClaimPOP3Host     string `env:"NICK_CLAIM_POP3_HOST" default:""`
	NickClaimPOP3Port     int    `env:"NICK_CLAIM_POP3_PORT"`
	NickClaimPOP3User     string `env:"NICK_CLAIM_POP3_USER" default:""`
	NickClaimPOP3Password string `env:"NICK_CLAIM_POP3_PASSWORD" default:""`
	NickClaimPOP3TLS      string `env:"NICK_CLAIM_POP3_TLS" default:""`

	// Legacy names for the inbound mailbox (PurelyMail on boson.chat).
	// Fallback values only: they no longer turn the feature on.
	PurelyMailPOP3Host     string `env:"PURELYMAIL_POP3_HOST" default:""`
	PurelyMailPOP3Port     int    `env:"PURELYMAIL_POP3_PORT" default:"995"`
	PurelyMailPOP3User     string `env:"PURELYMAIL_POP3_USER" default:""`
	PurelyMailPOP3Password string `env:"PURELYMAIL_POP3_PASSWORD" default:""`
	PurelyMailPOP3TLS      bool   `env:"PURELYMAIL_POP3_TLS" default:"true"`

	// Cloudflare R2 (S3-compatible) for user profile images. The avatar
	// service uploads resized images here and they're served via the CDN
	// at CloudflareCDNBaseURL (cdn.boson.chat). Avatar uploads no-op /
	// return an error when R2AccessKey is empty, so local-dev runs that
	// don't care about avatars don't need R2 configured. Real keys live in
	// the gitignored config.dev.json / prod env — never commit them.
	CloudflareR2AccessKey string `env:"CLOUDFLARE_R2_ACCESS_KEY" default:""`
	CloudflareR2SecretKey string `env:"CLOUDFLARE_R2_SECRET_KEY" default:""`
	CloudflareR2Endpoint  string `env:"CLOUDFLARE_R2_ENDPOINT" default:""`
	CloudflareR2Bucket    string `env:"CLOUDFLARE_R2_BUCKET" default:"boson"`
	CloudflareCDNBaseURL  string `env:"CLOUDFLARE_CDN_BASE_URL" default:"https://cdn.boson.chat"`
}

type DBConfig struct {
	Host     string `env:"DBHOST" default:"localhost"`
	Port     uint   `env:"DBPORT" default:"5432"`
	Database string `env:"DBNAME" default:"boson"`
	User     string `env:"DBUSERNAME" default:"boson"`
	Password string `env:"DBPASSWORD" default:"boson"`
	SSLMode  string `env:"DBSSLMODE" default:"disable"`
}

// AuthConfig points at the boson auth service (github.com/boson-chat/auth),
// which issues the EdDSA access tokens the API accepts. Defaults match the
// auth service's local dev setup (`make run` there, on :8080); production
// is https://auth.boson.chat.
type AuthConfig struct {
	// JWKS with the auth service's public signing keys.
	JWKSURL string `env:"AUTH_JWKS_URL" default:"http://localhost:8080/.well-known/jwks.json"`
	// Expected `iss`: the auth service's ISSUER (= its PUBLIC_URL).
	Issuer string `env:"AUTH_ISSUER" default:"http://localhost:8080"`
	// Expected `aud` of access tokens.
	Audience string `env:"AUTH_AUDIENCE" default:"boson"`
}

// LoadConfigOrPanic resolves config from the first matching JSON
// file under configFileSearchPaths() plus the process environment
// + struct-tag defaults. Missing JSON is fine (production), missing
// required-by-default isn't possible (every field has a default).
//
// Panics on a malformed JSON or a configor parse error so the
// failure surfaces at startup instead of producing a half-loaded
// Config silently.
func LoadConfigOrPanic() Config {
	var cfg Config

	// configor.Load(&cfg, paths...) walks the paths and merges
	// each existing file. Path resolution is CWD-relative, so we
	// try a few likely cwds (repo root, cmd/, cmd/server/) so
	// `make run` / `go run` from any of them still picks up the dev file.
	files := existingFiles(configFileSearchPaths())
	if err := configor.Load(&cfg, files...); err != nil {
		panic(err)
	}
	return cfg
}

// UsesDirectory reports whether ALLOWED_ORIGINS has "directory".
func (c AppConfig) UsesDirectory() bool {
	for _, e := range strings.Split(c.AllowedOrigins, ",") {
		if strings.EqualFold(strings.TrimSpace(e), "directory") {
			return true
		}
	}
	return false
}

// Validate checks settings that would otherwise fail confusingly later.
func (c Config) Validate() error {
	if c.AppConfig.UsesDirectory() && strings.TrimSpace(c.AppConfig.OriginsURL) == "" {
		return errors.New("ALLOWED_ORIGINS says directory: set ORIGINS_URL (the auth service's /v1/web-origins, e.g. http://auth:8080/v1/web-origins)")
	}
	return nil
}

// configFileSearchPaths returns candidate config-file paths in
// precedence order. configor merges all that exist; later files
// override earlier ones. Today we only ship config.dev.json (for
// local dev); production sets env vars directly.
func configFileSearchPaths() []string {
	return []string{
		"config/config.dev.json",       // running from repo root
		"../config/config.dev.json",    // running from cmd/
		"../../config/config.dev.json", // running from cmd/server/
	}
}

func existingFiles(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			// Skip duplicates so the same file (resolved via two
			// relative paths from different cwds) isn't merged
			// twice — harmless but noisy in logs.
			if absp, err := filepath.Abs(p); err == nil {
				seen := false
				for _, e := range out {
					if eabs, _ := filepath.Abs(e); eabs == absp {
						seen = true
						break
					}
				}
				if seen {
					continue
				}
			}
			out = append(out, p)
		}
	}
	return out
}
