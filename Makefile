.PHONY: setup tidy deps up down logs build stack-up stack-down run build-bin build-e2ecover \
        migrate-up migrate-down migrate-create reset \
        auth-up auth-down auth-logs auth-admin \
        dev-up dev-down ergo-up mailpit-up minio-up \
        test test-race vet fmt lint docker

setup: tidy up
	$(MAKE) migrate-up

tidy:
	go mod tidy

deps:
	go mod download

# Postgres only (the backend runs natively via `make run`).
up:
	docker compose up -d --wait postgres
	@echo "Postgres is up on localhost:5432"

down:
	docker compose down

logs:
	docker compose logs -f

# The api image via compose (same Dockerfile CI and releases build).
build:
	docker compose build api

stack-up:
	docker compose up -d --build

stack-down:
	docker compose down -v

run:
	go run -tags boson_dev ./cmd/server serve

build-bin:
	CGO_ENABLED=0 go build -trimpath -o bin/boson ./cmd/server

# Coverage-instrumented build for the real-app e2e suite (SIGUSR1 flushes
# counters to $$GOCOVERDIR; see cmd/server/coverflush_e2e.go).
build-e2ecover:
	go build -cover -tags boson_dev,boson_e2ecover -o bin/boson-cover ./cmd/server

migrate-up:
	go run ./cmd/server migrate up

migrate-down:
	go run ./cmd/server migrate down

migrate-create:
	@if [ -z "$(name)" ]; then echo "usage: make migrate-create name=create_users_table"; exit 1; fi
	@mkdir -p db/migrations
	@seq=$$(printf "%06d" $$(( $$(ls db/migrations 2>/dev/null | grep -E '^[0-9]+_' | tail -n 1 | cut -d_ -f1 | sed 's/^0*//' || echo 0) + 1 ))); \
	touch db/migrations/$${seq}_$(name).up.sql db/migrations/$${seq}_$(name).down.sql; \
	echo "Created db/migrations/$${seq}_$(name).{up,down}.sql"

reset: stack-down setup

# ----- boson auth service (local accounts + tokens) -----
#
# github.com/boson-chat/auth from a checkout next to this repo (override with
# AUTH_SRC=/path/to/auth). Public API on http://localhost:8080, its mail in
# the Mailpit at http://localhost:8026. The backend's defaults
# (AUTH_JWKS_URL / AUTH_ISSUER) point at it.

AUTH_SRC ?= ../auth

auth-up:
	@if [ ! -f "$(AUTH_SRC)/Dockerfile" ]; then \
		echo "No auth service checkout at $(AUTH_SRC)."; \
		echo "Clone it: gh repo clone boson-chat/auth $(AUTH_SRC)   (or set AUTH_SRC)"; \
		exit 1; \
	fi
	AUTH_SRC=$(AUTH_SRC) docker compose --profile dev-auth up -d --build --wait auth-postgres auth-mailpit auth
	@echo "auth service is up — API: http://localhost:8080  mail: http://localhost:8026"

auth-down:
	docker compose --profile dev-auth stop auth auth-mailpit auth-postgres

auth-logs:
	docker compose --profile dev-auth logs -f auth

# Create an admin account in the local auth service:
#   make auth-admin USERNAME=admin EMAIL=admin@example.com
auth-admin:
	@if [ -z "$(USERNAME)" ] || [ -z "$(EMAIL)" ]; then echo "usage: make auth-admin USERNAME=admin EMAIL=admin@example.com"; exit 1; fi
	docker compose --profile dev-auth exec auth ./auth user create --username "$(USERNAME)" --email "$(EMAIL)" --role admin

# ----- Combined dev flow -----

# Postgres + the auth service + ergo + mailpit (the nick-claim inbox) +
# MinIO (avatars), then migrations. Every step is idempotent.
dev-up: tidy up auth-up ergo-up mailpit-up minio-up
	@$(MAKE) migrate-up

dev-down: down

ergo-up: mailpit-up
	docker compose --profile testing up -d ergo
	@echo "ergo is up on localhost:6667 (mailto callback → mailpit:1025)"

# SMTP :1025, POP3 :1110 (dev:dev, matches .env.example), UI :8025.
mailpit-up:
	docker compose --profile dev-mail up -d mailpit
	@echo "mailpit is up — SMTP:1025  POP3:1110 (dev:dev)  UI: http://localhost:8025"

# Local S3 for avatars; point CLOUDFLARE_R2_* at it in config/config.dev.json.
minio-up:
	docker compose --profile dev-storage up -d minio minio-setup
	@echo "minio is up — S3:9000  console:9001 (minioadmin/minioadmin)  bucket: boson"

# ----- Tests + checks -----

# DB-backed tests need Postgres (make up; TEST_DB* env to point elsewhere)
# and skip when it's unreachable. -p 1 serialises packages because they
# share the boson_test database.
test:
	go test -p 1 ./...

test-race:
	go test -race -p 1 -count=1 ./...

vet:
	go vet ./...
	go vet -tags boson_dev,boson_e2ecover ./...

fmt:
	gofmt -w .

lint: vet
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

docker:
	docker build --build-arg VERSION=dev -t boson/backend:dev .
