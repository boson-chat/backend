## [0.57.3](https://github.com/boson-chat/backend/compare/v0.57.2...v0.57.3) (2026-11-24)

### Bug Fixes

* validate handles with IRC nick rules on POST/PATCH /me ([#8](https://github.com/boson-chat/backend/issues/8)) ([4670ecc](https://github.com/boson-chat/backend/commit/4670ecc58bd123d7da275f63d8dabc5516179e31))

## [0.57.2](https://github.com/boson-chat/backend/compare/v0.57.1...v0.57.2) (2026-11-21)

### Bug Fixes

* JWKS cache race when a token arrives with the keys loading ([#7](https://github.com/boson-chat/backend/issues/7)) ([1fa03a1](https://github.com/boson-chat/backend/commit/1fa03a14a6f4a4beb9d7216496a82a05cd3e5b93))

## [0.57.1](https://github.com/boson-chat/backend/compare/v0.57.0...v0.57.1) (2026-11-21)

### Bug Fixes

* retry the JWKS in the background when auth is down at startup ([#5](https://github.com/boson-chat/backend/issues/5)) ([3f38683](https://github.com/boson-chat/backend/commit/3f386833935da06a31afa91723b9b30398f91807))

## [0.57.0](https://github.com/boson-chat/backend/compare/v0.56.0...v0.57.0) (2026-11-21)

### Features

* GET /v1/unfurl link previews (site cards, YouTube, Spotify) with an SSRF-guarded fetcher ([#6](https://github.com/boson-chat/backend/issues/6)) ([31ac747](https://github.com/boson-chat/backend/commit/31ac747a223f9963bfceab1c0b78cb411c5b1296))

## [0.56.0](https://github.com/boson-chat/backend/compare/v0.55.1...v0.56.0) (2026-11-20)

### Features

* GET /v1/admin/profiles/{auth_user_id} for the admin panel's user page ([#3](https://github.com/boson-chat/backend/issues/3)) ([e5bd64c](https://github.com/boson-chat/backend/commit/e5bd64c232dcae02a383928291a346d079120061))

### Documentation

* license under AGPL-3.0-only ([#2](https://github.com/boson-chat/backend/issues/2)) ([483e79c](https://github.com/boson-chat/backend/commit/483e79caa8ead8ece084c66d99c69549d2fab392))

## [0.55.1](https://github.com/boson-chat/backend/compare/v0.55.0...v0.55.1) (2026-11-15)

### Build

* standalone backend repo ([#1](https://github.com/boson-chat/backend/issues/1)) ([6659de0](https://github.com/boson-chat/backend/commit/6659de0c4a3731d493352bfb65d911e9c04589bb))

Releases up to v0.55.0 were cut from the Boson monorepo, before the backend
moved to its own repository; their notes (which also cover the client,
desktop app and engine) are in
[boson-chat/boson's CHANGELOG](https://github.com/boson-chat/boson/blob/main/CHANGELOG.md).
