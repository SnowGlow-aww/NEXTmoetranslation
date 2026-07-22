# Locale And Lyrics Production Contract

This document records the additive contract implemented on top of legacy baseline `bbc3340`. The baseline behavior and fixtures in `LEGACY_BASELINE.md` remain authoritative for requests without an explicit locale and for all existing public files.

## Database Migrations

Migrations are applied in version order inside individual SQLite transactions. Each applied row records its version, name, SHA-256 checksum, and timestamp in `schema_migrations`; startup rejects changed or newer migration histories.

1. `multilingual_content_and_lyrics` adds locale side tables, stable event segments, catalog tables, lyrics drafts, line segments, and publication snapshots. It backfills only source text that the legacy schema actually retained.
2. `lyrics_source_provenance_and_stanzas` adds immutable source revision identity and stanza boundaries.
3. `lyrics_catalog_source_identity` adds catalog producer metadata used to reject ambiguous external source matches.
4. `rolling_event_side_tables_no_legacy_cascade` removes cascades from legacy story parents so a previous binary's replace-import cannot erase multilingual content. Current imports explicitly reconcile stable segment rows.
5. `stable_event_title_segment_identity` upgrades migrated title IDs to the same positional `:title:-1` identity used by current imports without losing existing localizations.
6. `mark_legacy_event_talk_identity` retains migrated talk positions as opaque `:legacy` IDs because filtered legacy output cannot truthfully identify `body` versus `speaker`; localization rows and source hashes move with those IDs. A current-format reimport reserves matching stable IDs first, then carries a localization to a different ID only when its source occurrence was unique before reimport and identifies exactly one unclaimed episode/kind/hash destination afterward.
7. `public_lyrics_attribution_and_token_generation` adds operator-authored public lyrics attribution and per-user token generations. Existing lyrics publication snapshots are cleared because private notes cannot be substituted for public attribution; drafts remain intact and require attribution before an administrator republishes them.

Before the first pending migration, file-backed databases receive a verified `*.pre-migration-vN.bak` SQLite backup. Migration tests cover idempotent reopen, checksum refusal, injected rollback, backup recovery, legacy writer compatibility, and old event replacement.

## Console APIs

The optional `locale` on `GET /api/categories`, `GET /api/entries`, `PUT /api/entry`, `GET /api/event-stories`, `GET /api/event-story`, and `PUT /api/event-story/update` accepts only `ja-JP`, `zh-CN`, and `en-US`. Omitted locale delegates to the exact legacy Chinese code path; explicit `zh-CN` event detail reads use the localized segment projection and its mutations and audit rows commit atomically without changing omitted-locale behavior; `ja-JP` is source-only and rejects writes; `en-US` is stored independently and is never changed by CN sync or AI translation. Localized event rows expose IDs and source hashes, and every explicit-locale write, including `zh-CN`, must echo and validate the full episode/type/key/ID/hash identity or receive a contract error or conflict.

Lyrics and catalog routes are authenticated:

- `GET /api/catalog/music`
- `GET /api/catalog/characters`
- `GET /api/lyrics` and `GET /api/lyrics/detail`
- `PUT /api/lyrics/save` for editors and admins
- `POST /api/lyrics/publish` and `POST /api/lyrics/unpublish` for admins
- `GET /api/lyrics/source/search` and `POST /api/lyrics/source/preview`

Lyrics use numeric `musicId`, stable ordered line IDs, Japanese/Chinese/English text, ordered text segments, required unique catalog performer IDs for publication, optimistic revisions, immutable Japanese source hashes and source provenance, draft/publication separation, and idempotent save/publish/unpublish behavior. Source lookup pins page and revision identity, rejects cross-origin redirects and explicit no-reprint markers/categories, and records source access in the audit log; contract errors use sanitized codes including `revision_conflict` (409), `source_drift`, `segment_mismatch`, `invalid_performer`, and `incomplete_publication` (422).

Public lyrics publication also requires non-empty operator-authored `attribution`. It is the only public provenance field: source notes, license notes, source URL, page/revision/SHA identity, fetch timestamps, editor identity, and draft status remain confined to authenticated APIs and backups. Canonical JSON Schemas and consumer fixtures for Moesekai are committed under `contracts/public-lyrics/v1/`; NEXT paths and field names remain authoritative and no duplicate compatibility editing endpoints are exposed.

JSON request bodies are capped at 8 MiB and the HTTP server bounds both header and whole-request read time. External lyrics-source requests use response caps, rate limiting, cache bounds, origin-locked redirects, and timeouts. HTTP responses carry a sanitized `X-Request-ID`; `/readyz` verifies SQLite readiness, admin-protected `/healthz/details` reports lightweight request/client-error/server-error counters, and `/healthz` retains its existing response. Locale, lyrics, source, and restore mutations/accesses write content-minimized audit rows.

## Public Files

Legacy `/files/translation/*` and `/translation/*` generation, ETags, CORS, and cache policy are unchanged. Additive assets include:

- `/files/v2/{locale}/translation/{category}.json`
- `/files/v2/{locale}/translation/{category}.full.json`
- `/files/v2/{locale}/translation/eventStory/event_{eventId}.json`
- `/files/v2/data/search-index.json` and `/files/v2/en-US/data/search-index.json`, retaining `n`/`cn` and adding optional `en`
- Published-only lyrics index/detail files under `/files/translation/lyrics/` and each locale's `/files/v2/{locale}/translation/lyrics/`

Lyrics assets rebuild as one set. A malformed publication preserves the complete previous set rather than exposing a partial index/detail update.

## Backup And Restore

Git and S3 backups retain the legacy public `translations` projection and add `translation-content/manifest.json`. Both projections are materialized from one SQLite snapshot; manifest schema version 1 checksums and counts multilingual entries, stable event content, catalog/lyrics drafts, source provenance, and publication snapshots. Legacy and additive restore data commit in one transaction, failure rolls everything back, and backups without the additive directory explicitly clear multilingual/lyrics-only state while retaining the old public restore projection. User tables, password hashes, settings, tokens, and secrets are not exported.

## Authentication And Operations

JWTs carry a persisted per-user token generation and are accepted only while the user still exists and the current database role and generation match. Password changes and role changes advance the generation, deletion removes the validation row, and all three operations immediately reject previously issued tokens. Public setup and login attempts are bounded in memory by both the normalized account and the socket `RemoteAddr`; forwarding headers are intentionally ignored. First-admin creation performs the empty-user check and insert in one immediate SQLite transaction.

Editors may proofread individual entries, event lines, and lyrics drafts. CN sync, every AI trigger, event retry/reorder/promote, backup push/restore, user/settings/upstream administration, and lyrics publication are admin-only; read-only status and catalog endpoints remain available to authenticated editors.

Production rollback is defined in `ROLLBACK_RUNBOOK.md`, including immutable artifact/image selection, database migration compatibility, credential/key handling, and post-rollback verification.

## Web Console

The existing setup/login/settings/AdminModal/CN sync/AI/backup/entry save/save-next/shortcuts surfaces remain mounted. Current imports derive event segment identity from event/scenario/episode plus invariant original scenario position and field (`body` or `speaker`), never from filtered JP/CN output order; migrated filtered IDs remain explicitly opaque until reconciled by a current import, and Japanese rows remain addressable when Chinese is missing or source-equal. The console adds a locale selector and filter-independent shared dirty guard for locale, field/category/mode, row, arrow, Escape, logout, AI/retry/reorder reloads, collaborator event reloads from both event SSE names, and browser-unload transitions; localized updates use locale-specific SSE event names that old Chinese clients ignore. The responsive lyrics workspace adds catalog search with stale-response suppression, source preview, performer selection, multilingual preview, optimistic conflict recovery, guarded song/publication transitions, draft save, and admin publication controls.

## Verification Record

Run on 2026-07-23 from this worktree:

| Command | Exit | Result |
| --- | ---: | --- |
| `cd server && gofmt -w $(git ls-files '*.go') $(git ls-files --others --exclude-standard '*.go') && go test ./...` | 0 | All Go packages passed |
| `cd server && go test -race ./...` | 0 | All Go packages passed under the race detector |
| `cd server && go vet ./...` | 0 | No diagnostics |
| `cd web && npm test` | 0 | Seven Web Console contract regressions passed |
| `cd web && npm ci` | 1 | Environment policy rejected remote `npmmirror.com` tarballs with `EALLOWREMOTE` |
| `cd web && npm ci --offline` | 1 | Same policy rejection; the required tarball was not available locally |
| `cd web && npm run lint` | 127 | `next` unavailable because dependency installation was blocked |
| `cd web && npm run build` | 127 | `next` unavailable because dependency installation was blocked |

No push, publish, deployment, production restore, live object-store/Git operation, or live external-source request was performed. Remaining verification risk is the unavailable Next.js dependency/toolchain; backend contracts, race tests, and dependency-free Web Console regression tests pass locally.
