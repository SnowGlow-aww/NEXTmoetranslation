#!/bin/sh
set -eu

expected_index=9a735e96f856da9b94e1362883df13616a8b6e3cd33afce5d5e1468b4784b475
expected_detail=e677b0df75ae407ab8a71510f8c081be209bea3ce56781a0d00a14e3e771858e
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
grep -q 'docker build' .github/workflows/ci.yml
grep -q 'docker image inspect' .github/workflows/ci.yml
grep -q 'docker run --rm' .github/workflows/ci.yml
grep -q 'actions/checkout@[0-9a-f]\{40\}' .github/workflows/ci.yml
grep -q 'actions/setup-go@[0-9a-f]\{40\}' .github/workflows/ci.yml
grep -q 'actions/setup-node@[0-9a-f]\{40\}' .github/workflows/ci.yml
grep -q 'actions/upload-artifact@[0-9a-f]\{40\}' .github/workflows/ci.yml
