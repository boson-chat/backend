package http

import (
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/boson-chat/backend/config"
	"github.com/boson-chat/backend/http/handlers"
	"github.com/boson-chat/backend/http/middleware"
	"github.com/boson-chat/backend/internal/clientip"
	"github.com/boson-chat/backend/internal/db"
	"github.com/boson-chat/backend/internal/logger"
	"github.com/boson-chat/backend/internal/ratelimit"
	"github.com/boson-chat/backend/internal/services/authidentity"
	"github.com/boson-chat/backend/internal/services/avatar"
	"github.com/boson-chat/backend/internal/services/bouncersecret"
	"github.com/boson-chat/backend/internal/services/nickclaim"
	"github.com/boson-chat/backend/internal/services/nickservsecret"
	"github.com/boson-chat/backend/internal/services/presence"
	sessionsvc "github.com/boson-chat/backend/internal/services/session"
	"github.com/boson-chat/backend/internal/services/user"
	"github.com/boson-chat/backend/pkg/unfurl"
)

func StartServerWithContext(ctx context.Context) error {
	cfg := config.LoadConfigOrPanic()
	database := db.NewDatabase(cfg.DBConfig)

	// Repositories
	userRepo := user.NewUserRepository(database)
	sessionRepo := sessionsvc.NewRepository(database)
	nickClaimRepo := nickclaim.NewRepository(database)
	nickservSecretRepo := nickservsecret.NewRepository(database)
	bouncerSecretRepo := bouncersecret.NewRepository(database)
	presenceRepo := presence.NewRepository(database)

	// Services
	userService := user.NewUserService(userRepo)
	sessionService := sessionsvc.NewService(sessionRepo)
	// Starts disabled; the nick-claim runtime below applies the real
	// settings (config file, then env) before the server listens.
	nickClaimService := nickclaim.NewService(nickClaimRepo, nickclaim.Config{})
	nickservSecretService := nickservsecret.NewService(nickservSecretRepo)
	bouncerSecretService := bouncersecret.NewService(bouncerSecretRepo)
	presenceService := presence.NewService(presenceRepo)

	// Avatar service — only wired when R2 is configured (access key present);
	// otherwise nil, and the avatar routes 503. Local dev without R2 still
	// boots fine.
	var avatarService avatar.ServiceImpl
	if cfg.AppConfig.CloudflareR2AccessKey != "" {
		r2 := avatar.NewR2Storage(
			cfg.AppConfig.CloudflareR2AccessKey,
			cfg.AppConfig.CloudflareR2SecretKey,
			cfg.AppConfig.CloudflareR2Endpoint,
			cfg.AppConfig.CloudflareR2Bucket,
		)
		avatarService = avatar.NewService(r2, cfg.AppConfig.CloudflareCDNBaseURL)
	}

	// Handlers
	meHandler := handlers.NewMeHandler(userService, avatarService)
	sessionHandler := handlers.NewSessionHandler(sessionService)
	nickClaimsHandler := handlers.NewNickClaimsHandler(nickClaimService)
	nickservSecretsHandler := handlers.NewNickServSecretsHandler(nickservSecretService)
	bouncerSecretHandler := handlers.NewBouncerSecretHandler(bouncerSecretService)
	presenceHandler := handlers.NewPresenceHandler(presenceService, avatarService)
	identities := authidentity.NewService(database)
	adminProfilesHandler := handlers.NewAdminProfilesHandler(userService, identities, avatarService)

	// Access tokens come from the boson auth service; auth_identities maps
	// their sub to boson's user ids (imported Supabase accounts keep theirs).
	tokens, err := middleware.NewVerifier(ctx, cfg.AuthConfig)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	// Public routes — health, the capability switches the client reads
	// (GET /config) and link previews (/v1/unfurl, signed in or not). The
	// server directory moved to the standalone directory service
	// (github.com/boson-chat/directory).
	publicMux := stdhttp.NewServeMux()
	publicMux.HandleFunc("GET /health", healthHandler(database))
	handlers.NewPublicConfigHandler(nickClaimService).Register(publicMux)
	if cfg.UnfurlConfig.Enabled {
		newUnfurlHandler(ctx, cfg, tokens).Register(publicMux)
	}

	// Protected routes — everything that needs an authenticated principal.
	protectedMux := stdhttp.NewServeMux()
	meHandler.Register(protectedMux)
	sessionHandler.Register(protectedMux)
	nickClaimsHandler.Register(protectedMux)
	nickservSecretsHandler.Register(protectedMux)
	bouncerSecretHandler.Register(protectedMux)
	presenceHandler.Register(protectedMux)
	adminProfilesHandler.Register(protectedMux)

	root := buildRouter(publicMux, protectedMux, middleware.RequireAuth(tokens, identities))

	// Nick claims: automatic NickServ registration on other networks.
	// Off unless the network admin enables it (nick_claim.enabled in the
	// stack config file, or NICK_CLAIM_ENABLED). The runtime starts and
	// stops the POP3 worker as the settings change; workers share ctx for
	// SIGTERM-driven shutdown.
	nickClaims := nickclaim.NewRuntime(ctx, nickClaimService, nickClaimRepo)
	defer nickClaims.Stop()
	configStatus, err := startNickClaimConfig(ctx, cfg, nickClaims)
	if err != nil {
		return err
	}

	if addr := cfg.InternalConfig.ListenAddr(); addr != "" {
		internalMux := stdhttp.NewServeMux()
		handlers.NewInternalHandler(nickClaims, cfg.InternalConfig.SharedToken(), configStatus).Register(internalMux)
		go serveInternal(ctx, addr, internalMux)
	}

	addr := fmt.Sprintf(":%d", cfg.AppConfig.Port)
	srv := &stdhttp.Server{
		Addr:              addr,
		Handler:           middleware.CORS(cfg.AppConfig.AllowedOrigins)(root),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log := logger.FromCtx(ctx)
	log.Info().Str("addr", addr).Msg("HTTP server listening")
	return srv.ListenAndServe()
}

// buildRouter wires the public and protected muxes onto a single root mux,
// applying `auth` only to routes that need an authenticated principal.
//
// /health and /config go to the public mux; every other path falls through to the
// auth-wrapped protected mux. Extracted so tests can verify the route-level auth gating without
// standing up the full server.
func buildRouter(
	publicMux, protectedMux *stdhttp.ServeMux,
	auth func(stdhttp.Handler) stdhttp.Handler,
) *stdhttp.ServeMux {
	authedProtected := auth(protectedMux)
	root := stdhttp.NewServeMux()
	root.Handle("/health", publicMux)
	root.Handle("/config", publicMux)
	root.Handle("/v1/unfurl", publicMux)
	root.Handle("/v1/unfurl/", publicMux)
	root.Handle("/", authedProtected)
	return root
}

// newUnfurlHandler wires GET /v1/unfurl*: the guarded fetcher and its
// cache (pkg/unfurl), per-address and per-user limits.
func newUnfurlHandler(ctx context.Context, cfg config.Config, tokens *middleware.Verifier) *handlers.UnfurlHandler {
	u := cfg.UnfurlConfig
	opts := unfurl.Options{CacheSize: u.CacheSize, CacheTTL: time.Duration(u.CacheTTLSeconds) * time.Second}
	if m := parseHostMap(u.TestHostMap); len(m) > 0 {
		if cfg.AppConfig.Env == "production" {
			logger.FromCtx(ctx).Warn().Msg("unfurl: UNFURL_TEST_HOST_MAP is ignored in production")
		} else {
			logger.FromCtx(ctx).Warn().Interface("hosts", m).Msg("unfurl: test host map in use (these names skip the SSRF guard)")
			opts.Fetcher.TestHostMap = m
		}
	}
	subject := func(tok string) (string, bool) {
		c, err := tokens.Verify(tok)
		if err != nil || !strings.HasPrefix(c.Subject, "usr_") {
			return "", false
		}
		return c.Subject, true
	}
	return handlers.NewUnfurlHandler(unfurl.NewService(opts), ratelimit.New(u.RatePerIP), ratelimit.New(u.RatePerUser), clientip.New(u.TrustedProxies), subject)
}

// parseHostMap reads "name=host[:port],…".
func parseHostMap(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		name, target, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok && strings.TrimSpace(name) != "" && strings.TrimSpace(target) != "" {
			m[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(target)
		}
	}
	return m
}

func healthHandler(database *db.DB) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		sqlDB, err := database.DB.DB()
		if err != nil {
			writeJSON(w, stdhttp.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
			return
		}
		if err := sqlDB.PingContext(r.Context()); err != nil {
			// Don't leak the DB ping error (it carries host/port/driver
			// internals) to unauthenticated /health callers; log it instead.
			log.Error().Err(err).Msg("health check: db ping failed")
			writeJSON(w, stdhttp.StatusServiceUnavailable, map[string]string{"status": "db_ping_failed"})
			return
		}
		writeJSON(w, stdhttp.StatusOK, map[string]string{"status": "ok"})
	}
}

func writeJSON(w stdhttp.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	// 204 No Content carries no body — skip the encode so we don't emit a
	// stray `null` on no-content responses.
	if status == stdhttp.StatusNoContent || body == nil {
		w.WriteHeader(status)
		return
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
