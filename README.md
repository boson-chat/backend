# Boson backend

The Go HTTP API behind the [Boson](https://boson.chat) desktop chat app
(`api.boson.chat`). It verifies access tokens issued by the
[boson auth service](https://github.com/boson-chat/auth) and stores what the
app syncs per account in Postgres:

- **Identity**: the user record, handle, display name, the encrypted identity
  secret and its recovery wrap (`/me`, `/me/secret-wraps`).
- **Profile images**: upload, resize and store in Cloudflare R2 (`/me/avatar`).
- **Sessions**: the saved client session (`/me/session`).
- **NickServ secrets and bouncer config**: end-to-end encrypted blobs the
  client stores and fetches (`/me/nickserv-secrets`, `/me/bouncer`).
- **Presence**: publish and look up who's online (`/me/presence`,
  `/presence/lookup`).
- **Nick claims**: automatic NickServ registration on other networks
  (`/me/nick-claims`), with a POP3 worker that reads confirmation codes from
  a dedicated inbound mailbox.

Public routes are `GET /health` and `GET /config`; everything else needs a
`Bearer` token from the auth service. A separate internal listener (`:3001`)
serves the stack config status and the admin panel's nick-claim endpoints.

Related repos: the app ([boson-chat/boson](https://github.com/boson-chat/boson)),
accounts and sign-in ([boson-chat/auth](https://github.com/boson-chat/auth)),
the server directory ([boson-chat/directory](https://github.com/boson-chat/directory)),
and the self-hosting compose stack ([boson-chat/stack](https://github.com/boson-chat/stack)),
which runs this image as its `api` service.

## Layout

```
cmd/server/          main (cobra CLI: serve, migrate, export-config, import-supabase-users)
config/              config structs (env + config/config.dev.json + defaults), nick-claim config
db/                  embedded golang-migrate migrations (db/migrations/*.sql)
http/                router, middleware (token verification), handlers
internal/commands/   CLI commands
internal/services/   one package per domain: repository + service
internal/configfile/ the stack config file (BOSON_CONFIG), watched live
internal/testutil/   test Postgres helper (creates + migrates boson_test)
infra/ergo/          local Ergo IRCd config for the nick-claim dev flow
```

## Run it locally

Prerequisites: Go ≥ 1.27, Docker with `docker compose`, and a checkout of
[boson-chat/auth](https://github.com/boson-chat/auth) next to this one
(`gh repo clone boson-chat/auth ../auth`, or set `AUTH_SRC`).

```bash
make dev-up   # Postgres :5432, auth service :8080, ergo :6667, mailpit, MinIO; then migrate up
make run      # the API on http://localhost:3000 (internal listener on :3001)
```

`make dev-up` is idempotent. For just the database: `make setup` (Postgres +
migrations). Other targets:

```bash
make migrate-up / migrate-down          # apply / roll back one migration
make migrate-create name=add_foo_table  # new db/migrations/NNNNNN_add_foo_table.{up,down}.sql
make auth-admin USERNAME=admin EMAIL=admin@example.com  # admin account in the local auth service
make stack-up                           # Postgres + the api container (built from this Dockerfile)
make docker                             # build the release image locally
make build-e2ecover                     # coverage-instrumented binary for the app's real-app e2e suite
```

Local services and their UIs:

| Service | Address | Notes |
| --- | --- | --- |
| Postgres | `localhost:5432` | `boson`/`boson`, database `boson` |
| Auth service | `http://localhost:8080` | its mail (verification, reset) in Mailpit at `http://localhost:8026` |
| Ergo IRCd | `localhost:6667` | plain TCP; registration mail goes to mailpit |
| Mailpit | SMTP `:1025`, POP3 `:1110` (`dev`/`dev`), UI `http://localhost:8025` | the nick-claim inbound mailbox |
| MinIO | S3 `:9000`, console `http://localhost:9001` (`minioadmin`/`minioadmin`) | stands in for R2; bucket `boson` |

## Configuration

Config comes from, in order of precedence: environment variables, then an
optional `config/config.dev.json` (gitignored; found from the repo root,
`cmd/` or `cmd/server/`), then defaults. The defaults match the local dev
stack, so `make run` works with no config. `.env.example` lists the common
variables.

| Variable | Default | |
| --- | --- | --- |
| `PORT` | `3000` | public API |
| `ENV` | `development` | |
| `ALLOWED_ORIGINS` | `http://localhost:5173` | CORS |
| `DBHOST` / `DBPORT` / `DBNAME` / `DBUSERNAME` / `DBPASSWORD` / `DBSSLMODE` | `localhost` / `5432` / `boson` / `boson` / `boson` / `disable` | Postgres |
| `AUTH_JWKS_URL` | `http://localhost:8080/.well-known/jwks.json` | auth service keys |
| `AUTH_ISSUER` | `http://localhost:8080` | expected `iss` |
| `AUTH_AUDIENCE` | `boson` | expected `aud` |
| `INTERNAL_PORT` / `INTERNAL_ADDR` | `3001` / unset | internal listener; `INTERNAL_ADDR` (`host:port` or `off`) wins. Never publish it. |
| `INTERNAL_TOKEN` | unset | `X-Internal-Token` for `/internal/nick-claims/*` (empty: those answer 503). `INTERNAL_API_TOKEN` is accepted too. |
| `BOSON_CONFIG` | unset | the stack config file (e.g. `/config/boson.yaml`), watched and applied live |
| `CLOUDFLARE_R2_ACCESS_KEY` / `_SECRET_KEY` / `_ENDPOINT` / `_BUCKET` | unset / unset / unset / `boson` | avatar storage; uploads answer 503 without an access key |
| `CLOUDFLARE_CDN_BASE_URL` | `https://cdn.boson.chat` | public base URL of stored avatars |
| `NICK_CLAIM_*` | see below | |

Never commit real keys: put them in the deployment env or the gitignored
`config/config.dev.json`.

### Nick claims

A signed-in user can have Boson register their nick on another IRC network:
the API mints a `reg+<userid>-<token>@<email_domain>` address
(`POST /me/nick-claims`), the client sends NickServ `REGISTER` with it, and
the POP3 worker drains the catch-all mailbox that address lands in and hands
the code back (`GET /me/nick-claims/{id}`). It is **off by default**. The
inbound mailbox must be **dedicated** to this: the worker deletes every
message it can't match to a claim.

Settings come from the `nick_claim:` section of the stack config file
(`BOSON_CONFIG`), else env, else defaults:

```yaml
nick_claim:
  enabled: false                 # NICK_CLAIM_ENABLED (default false)
  email_domain: example.com      # NICK_CLAIM_EMAIL_DOMAIN; reg+…@domain must reach the inbound mailbox
  rate_limit_per_hour: 5         # NICK_CLAIM_RATE_LIMIT_PER_HOUR
  inbound:
    protocol: pop3               # only pop3
    host: pop3.example.com       # NICK_CLAIM_POP3_HOST
    port: 995                    # NICK_CLAIM_POP3_PORT
    tls: true                    # NICK_CLAIM_POP3_TLS
    user: reg@example.com        # NICK_CLAIM_POP3_USER
```

The password is env only: `NICK_CLAIM_POP3_PASSWORD`. The older
`PURELYMAIL_POP3_*` names are still read as fallbacks but don't turn the
feature on. `enabled: true` needs `email_domain` and `inbound.host`.
`boson export-config` prints the section from the env (plus the file, if
`BOSON_CONFIG` exists) for moving an install to the file. While it is off,
`POST /me/nick-claims` answers `403 {"error":"nick_claim_disabled"}` and
`GET /config` says `{"nick_claim": false}`.

Internal listener endpoints: `GET /internal/config/status` (no token),
`GET /internal/nick-claims/status` and `POST /internal/nick-claims/test`
(with `X-Internal-Token`).

## Tests

```bash
make up          # Postgres for the DB-backed tests
make test        # go test -p 1 ./...
make test-race   # same with -race (what CI runs)
make lint        # go vet (both build-tag sets) + gofmt
```

DB-backed tests create and migrate a `boson_test` database on the test
server (`TEST_DBHOST` / `TEST_DBPORT` / `TEST_DBUSERNAME` / `TEST_DBPASSWORD`,
defaulting to the compose Postgres) and skip when it's unreachable. `-p 1`
is needed because packages share that database.

CI (`.github/workflows/ci.yml`) runs gofmt, `go mod tidy` drift, `go vet`,
the coverage e2e build, `go test -race` against a Postgres service, and a
Docker build on every PR and push to `main`.

## Releases

Merges to `main` are released by
[semantic-release](https://semantic-release.gitbook.io/)
(`.github/workflows/release.yml`, config in `.releaserc.json`) from the
conventional-commit messages since the last tag: `feat` → minor;
`fix`, `perf`, `refactor`, `build` and `docs(README)` → patch; `chore`, `ci`,
`style` and `test` → no release. It updates `CHANGELOG.md` (committed back
with `[skip ci]`), tags `vX.Y.Z` and creates the GitHub release. Squash-merge
PRs with a conventional title.

Each release then builds and pushes the multi-arch (linux/amd64 +
linux/arm64) image `harbor.floret.dev/boson/backend:<version>` and `:latest`
(`.github/workflows/release-docker.yml`, using the org secrets
`HARBOR_USERNAME` / `HARBOR_PASSWORD`). To rebuild a release image, run
"Release Docker images" by hand on its tag (or with a `version` input).
Deploy by setting the stack's `API_IMAGE` to the new tag.

Versions continue from the monorepo: releases up to v0.55.0 were cut from
boson-chat/boson.
