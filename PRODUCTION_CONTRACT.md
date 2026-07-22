# Locale And Lyrics Production Contract

This document records the additive contract implemented on top of legacy baseline `bbc3340`. The baseline behavior and fixtures in `LEGACY_BASELINE.md` remain authoritative for requests without an explicit locale and for all existing public files.

## Database Migrations

Migrations are applied in version order inside individual SQLite transactions. Each applied row records its version, name, SHA-256 checksum, and timestamp in `schema_migrations`; startup rejects changed or newer migration histories.

1. `multilingual_content_and_lyrics` adds locale side tables, stable event segments, catalog tables, lyrics drafts, line segments, and publication snapshots. It backfills only source text that the legacy schema actually retained.
2. `lyrics_source_provenance_and_stanzas` adds immutable source revision identity and stanza boundaries.
3. `lyrics_catalog_source_identity` adds catalog producer metadata used to reject ambiguous external source matches.
4. `rolling_event_side_tables_no_legacy_cascade` removes cascades from legacy story parents so a previous binary's replace-import cannot erase multilingual content. Current imports explicitly reconcile stable segment rows.

Before the first pending migration, file-backed databases receive a verified `*.pre-migration-vN.bak` SQLite backup. Migration tests cover idempotent reopen, checksum refusal, injected rollback, backup recovery, legacy writer compatibility, and old event replacement.

## Console APIs

The optional `locale` on `GET /api/categories`, `GET /api/entries`, `PUT /api/entry`, `GET /api/event-stories`, `GET /api/event-story`, and `PUT /api/event-story/update` accepts only `ja-JP`, `zh-CN`, and `en-US`. Omitted locale delegates to the exact legacy Chinese code path; `ja-JP` is source-only and rejects writes; `en-US` is stored independently and is never changed by CN sync or AI translation.

Lyrics and catalog routes are authenticated:

- `GET /api/catalog/music`
- `GET /api/catalog/characters`
- `GET /api/lyrics` and `GET /api/lyrics/detail`
- `PUT /api/lyrics/save` for editors and admins
- `POST /api/lyrics/publish` and `POST /api/lyrics/unpublish` for admins
- `GET /api/lyrics/source/search` and `POST /api/lyrics/source/preview`

Lyrics use numeric `musicId`, stable ordered line IDs, Japanese/Chinese/English text, ordered text segments, catalog performer IDs, optimistic revisions, immutable Japanese source hashes, draft/publication separation, and idempotent save/publish/unpublish behavior. Contract errors use sanitized codes including `revision_conflict` (409), `source_drift`, `segment_mismatch`, `invalid_performer`, and `incomplete_publication` (422).

JSON request bodies are capped at 8 MiB. External lyrics-source requests use response caps, rate limiting, cache bounds, and timeouts. HTTP responses carry a sanitized `X-Request-ID`; `/readyz` verifies SQLite readiness and `/healthz/details` reports lightweight request/client-error/server-error counters while `/healthz` retains its existing response.

## Public Files

Legacy `/files/translation/*` and `/translation/*` generation, ETags, CORS, and cache policy are unchanged. Additive assets include:

- `/files/v2/{locale}/translation/{category}.json`
- `/files/v2/{locale}/translation/{category}.full.json`
- `/files/v2/{locale}/translation/eventStory/event_{eventId}.json`
- `/files/v2/data/search-index.json` and `/files/v2/en-US/data/search-index.json`, retaining `n`/`cn` and adding optional `en`
- Published-only lyrics index/detail files under `/files/translation/lyrics/` and each locale's `/files/v2/{locale}/translation/lyrics/`

Lyrics assets rebuild as one set. A malformed publication preserves the complete previous set rather than exposing a partial index/detail update.

## Backup And Restore

Git and S3 backups retain the legacy public `translations` projection and add `translation-content/manifest.json`. Manifest schema version 1 checksums and counts multilingual entries, stable event content, catalog/lyrics drafts, source provenance, and publication snapshots. Restore validates the complete manifest before import; backups without the additive directory still follow the old restore path. User tables, password hashes, settings, tokens, and secrets are not exported.

## Web Console

The existing setup/login/settings/AdminModal/CN sync/AI/backup/entry save/save-next/shortcuts surfaces remain mounted. The console adds a locale selector whose dirty transition offers save, discard, and cancel; locale-scoped entry/event loading and SSE reconciliation; Japanese read-only rendering; and a responsive lyrics workspace with catalog search, source preview, performer selection, multilingual preview, optimistic conflict recovery, draft save, and admin publication controls.

## Verification Record

Run on 2026-07-22 from this worktree:

| Command | Exit | Result |
| --- | ---: | --- |
| `cd server && gofmt -w $(git ls-files '*.go') $(git ls-files --others --exclude-standard '*.go') && go test ./...` | 0 | All Go packages passed |
| `cd server && go test -race ./...` | 0 | All Go packages passed under the race detector |
| `cd server && go vet ./...` | 0 | No diagnostics |
| `cd web && npm test` | 0 | Four Web Console contract regressions passed |
| `cd web && npm ci` | 1 | Environment policy rejected remote `npmmirror.com` tarballs with `EALLOWREMOTE` |
| `cd web && npm ci --offline` | 1 | Same policy rejection; the required tarball was not available locally |
| `cd web && npm run lint` | 127 | `next` unavailable because dependency installation was blocked |
| `cd web && npm run build` | 127 | `next` unavailable because dependency installation was blocked |

No push, publish, deployment, production restore, live object-store/Git operation, or live external-source request was performed. Remaining verification risk is the unavailable Next.js dependency/toolchain; backend contracts, race tests, and dependency-free Web Console regression tests pass locally.
