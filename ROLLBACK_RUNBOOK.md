# Production Rollback Runbook

## Trigger And Ownership

Use this procedure for a bad application artifact or container image, a failed rollout, or a schema-related startup failure. Assign one incident owner, stop automated deploys, backup jobs, CN sync, AI translation, and all editor writes, and record the current artifact digest, Git revision, database path, database migration version, and UTC time before changing anything.

## Preserve Evidence And Recovery Inputs

1. Keep the failed immutable artifact or image digest available for diagnosis; never overwrite its tag.
2. Create a full SQLite snapshot with the online backup API, `.backup`, or `VACUUM INTO`, then run `PRAGMA integrity_check`, encrypt it, and transfer it to independently controlled off-host storage before rollback. Do not copy only the main file while WAL writes are active. The application Git/S3 `translation-content` backup is not sufficient because it omits users, password hashes, token generations, settings, audit state, and encrypted configuration.
3. Retain the automatic `*.pre-migration-vN.bak` file and verify it opens before relying on it.
4. Preserve application logs, request IDs, deployment events, and the exact environment-variable names in use. Do not place secret values in tickets or logs.
5. Download the CI artifact named `rollback-<full-git-sha>`, which contains only `moesekai-rollback.tar`. Extract it into an empty directory, verify `sha256sum -c SHA256SUMS`, and confirm `moesekai-server` and `moesekai-migrate` retain mode `0755` before use. The deterministic tar contains both binaries, the complete exported `web/` tree, and its internal checksums; CI builds it twice and requires byte equality. CI artifacts are never overwritten; container deployments must separately record the digest-qualified three base images and final image digest.

## Artifact Or Image Rollback

1. Select the last known-good final image digest from the paired-release record, not a tag. Verify its OCI labels and custom paired predicate agree on the NEXT revision, Moe tag/revision, workspace archive digest, workspace manifest digest, and final image digest. Retain its paired `WORKSPACE_MANIFEST_SHA256` and confirm the manifest's producer `sourceRevision` is the intended immutable SekaiText-Moe commit. Pair tags are discovery aids only and must never be rewritten during rollback.
2. Use a `Recreate` rollout for this service: stop the current instance before starting the replacement. The editor gate and SQLite writer are process-local, so two backend replicas must never serve concurrently. A separately replicated static frontend must not imply replicated backend writers.
3. Deploy the known-good artifact with the same persistent data mount and network policy only after completing the database compatibility decision below.
4. Keep the failed version unavailable to traffic until verification completes.

## Database Compatibility Decision

1. Read `schema_migrations` from the affected database and compare its highest version, name, and checksum with the rollback binary.
2. If the rollback binary recognizes the current migration history and its documented behavior is compatible, retain the current database. Never edit `schema_migrations` to make an old binary start.
3. If the rollback binary is older than the database history, restore the verified pre-migration backup created immediately before the first unsupported migration. Do not downgrade tables in place.
4. Migration v9 (`retain_rolling_event_segment_recovery_rows`) changes event segment uniqueness. A binary that does not recognize v9 must not open the v9 database. Stop the service and restore the verified backup created before the first pending migration in that rollout: this is `*.pre-migration-v9.bak` only when v9 was the first pending migration, and may instead be `*.pre-migration-v8.bak` for a v7→v9 jump. If that recovery point is too old, use the pre-rollout full off-host snapshot. Then apply the token invalidation step below.
5. Restoring a database also restores user token generations, drafts, publications, and audit state to that point. Before restarting, invalidate every token that the restore could revive: either rotate `JWT_SECRET` to fresh signing material or, in one audited transaction against the stopped database, increment `users.token_version` for every user. Record this security and data-loss window and require all users to reauthenticate. Do not return traffic until one of these invalidation steps is verified.
6. After database restore, regenerate public assets from that database; do not combine public files from one revision with SQLite state from another.

## Keys And Credentials

1. Keep `MOESEKAI_MASTER_KEY` stable when retaining or restoring encrypted settings; changing it makes existing encrypted values unreadable. Production keys contain at least 32 bytes of secret material and should be generated once with `openssl rand -base64 32`, then retained in the deployment secret manager.
2. Keep `JWT_SECRET` stable only for an ordinary artifact rollback that retains the current database. A database restore requires the JWT rotation or global `users.token_version` increment above; suspected signing-material exposure always requires rotation. Require all users to log in again after either event.
3. Do not restore revoked Git, S3, upstream, or LLM credentials from an old environment snapshot. Reapply the current secret-manager values and least-privilege policies after rollback.
4. Verify Git backup branch protections and S3 bucket/versioning policy before re-enabling backup push.

## Verification Before Traffic

1. Verify the selected digest's keyless image signature and custom paired attestation, and verify the source workspace archive's Sigstore bundle against issuer `https://token.actions.githubusercontent.com` and the exact official producer tag workflow identity. Run `moesekai-server --verify-workspace` with the release `WORKSPACE_WEB_DIR`, `WORKSPACE_MANIFEST_SHA256`, and `MOESEKAI_PRODUCTION=true`; do not proceed unless schema v3 verifies `sourceDirty=false`, `sourceProduction=true`, the recorded Moe revision, exact route authorization, and the closed-world inventory. Then start one isolated instance and require `/readyz` success only after the asynchronous initial public projection publishes; check independent `/healthz` liveness and admin-authenticated `/healthz/details` without exposing counters publicly. Confirm all operational responses are `Cache-Control: no-store`.
2. Confirm the expected `schema_migrations` rows and open the database with `PRAGMA integrity_check` through an approved operator tool.
3. Log in as an editor and an administrator. Confirm stale tokens from changed/deleted accounts fail, editor proofreading works, and editor requests to sync, AI, retry/reorder, and backup push return `403`.
4. Load `GET /api/editor-gate/status`, verify its process-specific `instanceId`, and exercise a strict `/api/editor/v1/*` write with `X-Moe-Loaded-Producer-State`. Confirm a stale pre-restart instance or completed generation returns `409` with current status, a missing header returns `428`, and canonical clients remain compatible. Do not restore traffic if the gate reports `running` or its counters violate `completedGeneration <= generation`.
5. Fetch representative legacy no-locale category and event assets and compare their shape to the compatibility fixtures.
6. Fetch the public lyrics index and one `music_{id}.json` detail, validate them against `contracts/public-lyrics/v1/`, confirm public attribution is present, and confirm private source fields are absent.
7. Perform a dry-run or non-pushing content-backup materialization, verify checksums, and confirm the lyrics attribution round-trips. Separately restore the encrypted full SQLite snapshot in isolation, run `PRAGMA integrity_check`, and verify users, settings, audit rows, and token generations are present.
8. Restore traffic gradually to the single backend instance, monitor request IDs and error counters, then re-enable backup, sync, and AI jobs one at a time.

## Forward Recovery

Prefer a fixed forward release after service is stable. It must accept the restored/current migration history, rerun all uncached Go, race, vet, Web contract, formatting, and diff checks, and use a new immutable artifact or image digest. Record the rollback and forward-recovery revisions, database decision, credential rotations, and verification evidence in the incident log.
