# Paired Image Release Runbook

## Scope

`.github/workflows/release-paired.yml` is the only production workspace-to-image publication path. It is manually dispatched from the protected default branch with:

- `moe_tag`: the exact official producer tag, formatted as `v` followed by strict SemVer, for example `v5.9.6`.
- `moe_commit`: the exact 40-character lowercase commit published by that tag.

The workflow checks out the dispatch's exact NEXT commit, requires that the dispatch ref is the repository default branch, and runs behind the `production` environment. Configure the environment to permit only protected branches and require the intended reviewers. Repository-wide concurrency permits only one paired release for this repository at a time. The workflow publishes an image and attestations but has no deploy step.

## Required Repository Configuration

Production environment or repository variables:

| Name | Requirement |
| --- | --- |
| `MOE_RELEASE_READER_APP_ID` | App ID for a GitHub App installed on owner `SnowGlow-aww` and repository `SekaiText-Moe` only. The installation and generated token must grant only repository metadata plus `Contents: read`. |
| `NODE_IMAGE_DIGEST` | Approved 64-character lowercase SHA-256 for `node:20.19.4-alpine3.22`. |
| `GO_IMAGE_DIGEST` | Approved 64-character lowercase SHA-256 for `golang:1.25.1-alpine3.22`. |
| `RUNTIME_IMAGE` | Approved runtime image name, without an embedded `@` digest; it must already contain pinned Git, CA certificates, and timezone data. |
| `RUNTIME_IMAGE_DIGEST` | Approved 64-character lowercase SHA-256 for `RUNTIME_IMAGE`. |
| `GITHUB_ARTIFACT_ATTESTATIONS_ENABLED` | Optional. Set exactly `true` only if the private repository and plan support GitHub artifact attestations. Omit or set false otherwise. |

Production environment secret:

| Name | Requirement |
| --- | --- |
| `MOE_RELEASE_READER_PRIVATE_KEY` | Private key for the release-reader GitHub App. Do not reuse a broad organization App or a personal access token. |

The workflow's ordinary `GITHUB_TOKEN` needs `packages: write`, `attestations: write`, `id-token: write`, and `contents: read` as declared in the job. Organization policy must allow GitHub Actions to publish to the target GHCR package.

Registry controls are mandatory, not advisory. Configure the GHCR package so final pair tags cannot be deleted or rewritten, and restrict package-writer access to this protected workflow's repository token. Every workflow that can write this package must use the same repository-wide concurrency group or be disabled. Workflow concurrency closes races among conforming runs; registry immutability and package-writer restriction close races from other writers between the final precheck, promotion, and postcheck. Do not run this workflow until all three controls are enforced.

## Producer Input

The private `SnowGlow-aww/SekaiText-Moe` release may also contain desktop installers. NEXT never lists them in logs or downloads them. It requires the official Releases API response to have `immutable: true`, resolves the exact official Git tag ref with the same narrow GitHub App token, recursively peels annotated tags, and requires the resulting commit to equal `moe_commit` before downloading anything. It then selects only this exact five-file, commit-addressed set and rejects a missing file, duplicate, incomplete upload, mutable release, or any other asset with the workspace prefix:

```text
sekaitext-moe-web-workspace-<moe_commit>.tar.gz
sekaitext-moe-web-workspace-<moe_commit>.tar.gz.sha256
sekaitext-moe-web-workspace-<moe_commit>.tar.gz.manifest.sha256
sekaitext-moe-web-workspace-<moe_commit>.tar.gz.commit
sekaitext-moe-web-workspace-<moe_commit>.tar.gz.sigstore.json
```

The GitHub App token is short-lived, scoped by the action to that one repository and `contents: read`, used only for tag objects, release metadata, and those five downloads, and revoked by the action after the job. Before each download, NEXT bounds release metadata to 256 MiB for the archive, 16 MiB for the Sigstore bundle, exactly 41 bytes for the commit sidecar, exactly 65 bytes for the manifest digest, and the exact commit-addressed length for the archive digest sidecar. Each download goes to a partial file, and its byte count must exactly equal the selected immutable release metadata before it is renamed or consumed.

## Verification And Extraction

`server/cmd/paired-release` is NEXT-owned code. The workflow never executes producer scripts. It requires exact local filenames and regular files, validates the commit sidecar, archive SHA-256 sidecar, canonical manifest SHA-256 sidecar, and verifies the archive bundle with:

```text
OIDC issuer: https://token.actions.githubusercontent.com
certificate identity: https://github.com/SnowGlow-aww/SekaiText-Moe/.github/workflows/release.yml@refs/tags/<moe_tag>
GitHub workflow SHA: <moe_commit>
```

Before writing archive contents, it rejects absolute paths, traversal, backslashes, duplicate paths, case-colliding components, links, devices, nonregular entries, unexpected top-level paths, trailing archive data/members, and file/count/expanded-size bounds violations. It extracts into a newly created directory, strips only `dist-web-workspace/`, and removes partial output on failure. NEXT then runs its own schema-v3 verifier in production mode against the manifest sidecar digest, including exact producer repository and `moe_commit`, `sourceDirty=false`, `sourceProduction=true`, route contracts, and closed-world file/directory inventory.

The Cosign bundle is authoritative for the private producer artifact when GitHub artifact attestations are unavailable. Plugin-market and updater signing keys are not accepted for this release identity.

## Image Publication

The Docker build uses the verified extracted directory as BuildKit named context `workspace` and builds the Dockerfile's default `paired` target. All three base references are digest-pinned. The build first pushes only one run-scoped staging tag:

```text
ghcr.io/<next-repository>:staging-<github-run-id>-<github-run-attempt>
```

The workflow requires that staging tag to be absent before the build and to resolve to the build action's exact digest afterward. It then signs that digest and creates the mandatory custom attestation. Both `cosign verify` and `cosign verify-attestation` require the exact NEXT workflow certificate identity, GitHub Actions issuer, and `--certificate-github-workflow-sha <next-full-sha>`. NEXT decodes every verified DSSE statement and requires its predicate type, image subject/digest, and complete predicate JSON to match the locally generated pair predicate.

Only after all mandatory signing and verification succeeds does the workflow promote the digest to these two final pair-specific tags:

```text
ghcr.io/<next-repository>:next-<next-full-sha>-moe-<moe-tag>
ghcr.io/<next-repository>:next-<next-full-sha>-moe-<moe-full-sha>
```

No `latest` tag is created. A failed sign, attestation, signature verification, or predicate validation leaves no final pair tags. Promotion accepts an existing final tag only when it already resolves to the exact verified digest, rejects a different digest, and rechecks both tags after promotion. OCI labels record the NEXT revision, Moe tag/revision, archive digest, and manifest digest. The final image is keylessly signed with GitHub OIDC and receives a mandatory Cosign custom attestation of predicate type `https://github.com/StarMoe-org/NEXTmoetrabslation/attestations/paired-image/v1`. That predicate binds both repositories/revisions, the Moe tag, source archive/manifest digests, final image name, and final image digest.

If `GITHUB_ARTIFACT_ATTESTATIONS_ENABLED=true`, the same digest also receives standard GitHub build provenance and a GitHub custom paired attestation. Those steps fail closed when explicitly enabled. The workflow summary and `publish` job output report the final `sha256:` image digest; record that digest for rollout and rollback rather than selecting by tag alone.

## Operator Procedure

1. Confirm GitHub reports the Moe release as **Immutable**, its protected tag resolves to the intended commit, and it exposes the exact five workspace assets.
2. Confirm the selected NEXT default-branch commit's latest push CI passed branch protection. Record the approved `NODE_IMAGE_DIGEST`, `GO_IMAGE_DIGEST`, and `RUNTIME_IMAGE`/`RUNTIME_IMAGE_DIGEST` values from that CI run, then confirm the `production` environment/repository variables still match exactly before approving release; the current source gate binds the NEXT commit but does not automatically compare those historical CI inputs.
3. Dispatch `Release paired image` with the exact tag and full commit. Approve the `production` environment only after reviewing both revisions and the unchanged base-image inputs.
4. Record the final image digest, both source revisions, Moe tag, archive digest, manifest digest, workflow run URL, and attestation URLs or Cosign verification evidence.
5. Independently verify labels, the keyless image signature, and paired predicate against the final digest before a separate deployment process uses it.

Run local release checks with:

```bash
cd server && go test ./cmd/paired-release ./internal/workspaceverify
cd .. && node --test scripts/release-workflow.test.mjs && ./scripts/verify-release.sh
```

Local tests synthesize malformed archives and signatures only for verifier coverage. Production pairing always consumes the official release's existing workspace archive; there is no separately built contract-fixture image publication path.
