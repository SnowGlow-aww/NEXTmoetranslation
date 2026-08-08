#!/bin/sh
set -eu

expected_index=9a735e96f856da9b94e1362883df13616a8b6e3cd33afce5d5e1468b4784b475
expected_detail=224a7d34e1d4d551bca21cbe70374f504a781edef90eb644d8d4ec9e5fca064c
expected_db=2eb61967a5f5b96a4961c0258984d6d5bb2f7b813379872d9d50a427704b8877

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

test "$(hash_file contracts/public-lyrics/v1/index.fixture.json)" = "$expected_index"
test "$(hash_file contracts/public-lyrics/v1/detail.fixture.json)" = "$expected_detail"
test "$(hash_file server/internal/db/testdata/legacy-v2.db)" = "$expected_db"

if grep -q 'registry\.npmmirror\.com' web/package-lock.json; then
  echo "package lock contains a noncanonical registry" >&2
  exit 1
fi
for image_arg in NODE_IMAGE NODE_IMAGE_DIGEST GO_IMAGE GO_IMAGE_DIGEST RUNTIME_IMAGE RUNTIME_IMAGE_DIGEST; do
  grep -q "ARG $image_arg" Dockerfile
done
grep -q 'FROM ${NODE_IMAGE}@sha256:${NODE_IMAGE_DIGEST}' Dockerfile
grep -q 'FROM ${GO_IMAGE}@sha256:${GO_IMAGE_DIGEST}' Dockerfile
grep -q 'FROM ${RUNTIME_IMAGE}@sha256:${RUNTIME_IMAGE_DIGEST}' Dockerfile
grep -q '^USER 65532:65532$' Dockerfile
grep -q 'chown -R 0:0 /app' Dockerfile
grep -q 'chmod -R a-w /app' Dockerfile
grep -q 'FROM runtime AS paired' Dockerfile
grep -q 'COPY --from=workspace --chown=0:0 --chmod=0555' Dockerfile
grep -q 'WORKSPACE_MANIFEST_SHA256' Dockerfile
grep -q 'WORKSPACE_ARCHIVE_SHA256' Dockerfile
grep -q 'io.sekaitext.pair.next.revision' Dockerfile
grep -q 'io.sekaitext.pair.moe.revision' Dockerfile
grep -q 'io.sekaitext.pair.workspace.archive.digest' Dockerfile
grep -q 'io.sekaitext.pair.workspace.manifest.digest' Dockerfile
grep -q "grep -Eq '\^\[0-9a-f\]{64}\$'" Dockerfile
grep -q "grep -Eq '\^\[0-9a-f\]{40}\$'" Dockerfile
grep -q 'MOE_TAG.*grep -Eq' Dockerfile
grep -q 'moesekai-server --verify-workspace' Dockerfile
grep -q 'MOESEKAI_PRODUCTION=true' Dockerfile
grep -q '"schemaVersion": 3' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json
grep -q '"name": "sekaitext-moe-loaded-producer-state"' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json
grep -q '"name": "sekaitext-moe-editor-gate"' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json
grep -q '"mutationHeaderFormat": "<base64url-instanceId>:<revision>:<completedGeneration>"' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json
if [ "$(grep -c '"version": 2' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json)" -lt 2 ]; then
  echo "workspace manifest proof contracts are not both version 2" >&2
  exit 1
fi
grep -q '"sourceProduction": true' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json
grep -q '"authentication": "bearer"' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json
if grep -q '"query-token"' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json; then
  echo "workspace manifest still permits query-token authentication" >&2
  exit 1
fi
if grep -R -q '/sse?token=' web/src; then
  echo "web console still places a session token in the SSE URL" >&2
  exit 1
fi
grep -q 'Authorization.*Bearer' web/src/lib/fetch-sse.mjs
grep -q '"producerProof": true' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json
grep -q '"allowedRoles": \[' server/internal/workspaceverify/testdata/valid/web-workspace-manifest.json
grep -q 'docker build' .github/workflows/ci.yml
grep -q -- '--target runtime' .github/workflows/ci.yml
grep -q -- '--target paired' .github/workflows/ci.yml
grep -q -- '--build-context workspace=' .github/workflows/ci.yml
grep -q -- '--build-arg WORKSPACE_ARCHIVE_SHA256=' .github/workflows/ci.yml
grep -q -- '--build-arg NEXT_REVISION=' .github/workflows/ci.yml
grep -q -- '--build-arg MOE_REVISION=' .github/workflows/ci.yml
grep -q -- '--build-arg MOE_TAG=' .github/workflows/ci.yml
grep -q 'docker image inspect' .github/workflows/ci.yml
grep -q 'docker run --rm' .github/workflows/ci.yml
grep -q '! chmod u+w /app' .github/workflows/ci.yml
grep -q '! mv /app/workspace /app/workspace-renamed' .github/workflows/ci.yml
grep -q '! printf x >> /app/workspace/assets/app.js' .github/workflows/ci.yml
grep -q "grep -Fx 'MOESEKAI_PRODUCTION=true'" .github/workflows/ci.yml
grep -q 'Assert paired production startup fails closed' .github/workflows/ci.yml
grep -q -- '--read-only' .github/workflows/ci.yml
grep -q -- '--tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m' .github/workflows/ci.yml
grep -q 'database is already owned by another process' .github/workflows/ci.yml
grep -q 'docker stop --time 30' .github/workflows/ci.yml
grep -q 'CRITICAL: total shutdown budget' .github/workflows/ci.yml
grep -q 'shutdown completed after forced cancellation' .github/workflows/ci.yml
grep -q 'force-close HTTP:' .github/workflows/ci.yml
grep -q 'seed-incomplete' docker-entrypoint.sh
grep -q 'package-rollback.sh' .github/workflows/ci.yml
grep -q 'cmp dist/moesekai-rollback.tar' .github/workflows/ci.yml
grep -q 'sha256sum -c SHA256SUMS' .github/workflows/ci.yml
grep -q 'path: dist/moesekai-rollback.tar' .github/workflows/ci.yml
test -x scripts/package-rollback.sh
grep -q '"$origin/readyz"' .github/workflows/ci.yml
grep -q '"$origin/api/does-not-exist"' .github/workflows/ci.yml
if grep -q 'seed migration failed; starting with empty database' docker-entrypoint.sh; then
  echo "entrypoint still fails open after seed migration errors" >&2
  exit 1
fi
grep -q 'actions/checkout@[0-9a-f]\{40\}' .github/workflows/ci.yml
grep -q 'actions/setup-go@[0-9a-f]\{40\}' .github/workflows/ci.yml
grep -q 'actions/setup-node@[0-9a-f]\{40\}' .github/workflows/ci.yml
grep -q 'actions/upload-artifact@[0-9a-f]\{40\}' .github/workflows/ci.yml
test -f .github/workflows/release-paired.yml
test -f server/cmd/paired-release/main.go
node --test scripts/release-workflow.test.mjs
