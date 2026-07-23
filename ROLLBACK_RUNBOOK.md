# Production Rollback Runbook

## Trigger And Ownership

Use this procedure for a bad application artifact or container image, a failed rollout, or a schema-related startup failure. Assign one incident owner, stop automated deploys, backup jobs, CN sync, AI translation, and all editor writes, and record the current artifact digest, Git revision, database path, database migration version, and UTC time before changing anything.

## Preserve Evidence And Recovery Inputs

1. Keep the failed immutable artifact or image digest available for diagnosis; never overwrite its tag.
2. Copy the SQLite database with SQLite backup semantics, including a verification open, before rollback. Do not copy only the main file while WAL writes are active.
3. Retain the automatic `*.pre-migration-vN.bak` file and verify it opens before relying on it.
4. Preserve application logs, request IDs, deployment events, and the exact environment-variable names in use. Do not place secret values in tickets or logs.
5. Download the CI artifact named `rollback-<full-git-sha>` and verify `SHA256SUMS` before use. CI artifacts are never overwritten; container deployments must separately record the digest-qualified three base images and final image digest.

## Artifact Or Image Rollback

1. Select the last known-good Git commit and immutable image digest from the deployment record, not a mutable tag.
2. Stop the current instance before switching a single-instance SQLite deployment. For a replicated frontend, drain bad instances before replacing them.
3. Deploy the known-good artifact with the same persistent data mount and network policy only after completing the database compatibility decision below.
4. Keep the failed version unavailable to traffic until verification completes.

## Database Compatibility Decision

1. Read `schema_migrations` from the affected database and compare its highest version, name, and checksum with the rollback binary.
2. If the rollback binary recognizes the current migration history and its documented behavior is compatible, retain the current database. Never edit `schema_migrations` to make an old binary start.
3. If the rollback binary is older than the database history, restore the verified pre-migration backup created immediately before the first unsupported migration. Do not downgrade tables in place.
4. Restoring a database also restores user token generations, drafts, publications, and audit state to that point. Before restarting, invalidate every token that the restore could revive: either rotate `JWT_SECRET` to fresh signing material or, in one audited transaction against the stopped database, increment `users.token_version` for every user. Record this security and data-loss window and require all users to reauthenticate. Do not return traffic until one of these invalidation steps is verified.
5. After database restore, regenerate public assets from that database; do not combine public files from one revision with SQLite state from another.

## Keys And Credentials

1. Keep `MOESEKAI_MASTER_KEY` stable when retaining or restoring encrypted settings; changing it makes existing encrypted values unreadable.
2. Keep `JWT_SECRET` stable only for an ordinary artifact rollback that retains the current database. A database restore requires the JWT rotation or global `users.token_version` increment above; suspected signing-material exposure always requires rotation. Require all users to log in again after either event.
3. Do not restore revoked Git, S3, upstream, or LLM credentials from an old environment snapshot. Reapply the current secret-manager values and least-privilege policies after rollback.
4. Verify Git backup branch protections and S3 bucket/versioning policy before re-enabling backup push.

## Verification Before Traffic

1. Start one isolated instance and require `/readyz` success; check `/healthz` and admin-authenticated `/healthz/details` without exposing counters publicly.
2. Confirm the expected `schema_migrations` rows and open the database with `PRAGMA integrity_check` through an approved operator tool.
3. Log in as an editor and an administrator. Confirm stale tokens from changed/deleted accounts fail, editor proofreading works, and editor requests to sync, AI, retry/reorder, and backup push return `403`.
4. Fetch representative legacy no-locale category and event assets and compare their shape to the compatibility fixtures.
5. Fetch the public lyrics index and one `music_{id}.json` detail, validate them against `contracts/public-lyrics/v1/`, confirm public attribution is present, and confirm private source fields are absent.
6. Perform a dry-run or non-pushing backup materialization, verify checksums, and confirm the lyrics attribution round-trips in additive content.
7. Restore traffic gradually, monitor request IDs and error counters, then re-enable backup, sync, and AI jobs one at a time.

## Forward Recovery

Prefer a fixed forward release after service is stable. It must accept the restored/current migration history, rerun all uncached Go, race, vet, Web contract, formatting, and diff checks, and use a new immutable artifact or image digest. Record the rollback and forward-recovery revisions, database decision, credential rotations, and verification evidence in the incident log.
