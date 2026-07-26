# Build from the REPOSITORY ROOT (this directory):
#
#   docker build \
#     --build-arg NODE_IMAGE=node:20.19.4-alpine3.22 \
#     --build-arg NODE_IMAGE_DIGEST=<64-hex-digest> \
#     --build-arg GO_IMAGE=golang:1.25.1-alpine3.22 \
#     --build-arg GO_IMAGE_DIGEST=<64-hex-digest> \
#     --build-arg RUNTIME_IMAGE=<approved-runtime-with-git> \
#     --build-arg RUNTIME_IMAGE_DIGEST=<64-hex-digest> \
#     --build-context workspace=/path/to/verified/sekaitext-moe-workspace \
#     --build-arg WORKSPACE_MANIFEST_SHA256=<manifest-sha256> \
#     --build-arg VERSION=<release> --build-arg VCS_REF=$(git rev-parse HEAD) \
#     -t moesekai-v2 .
#
# A .dockerignore keeps host-built web/node_modules and web/.next out of the
# context so they cannot clobber the Linux artifacts produced in the builders.
#
# Architecture: ONE process. The Go backend serves the statically-exported
# console SPA at "/" and the API/SSE/files at /api, /sse, /files. No nginx, no
# Node.js at runtime.

ARG NODE_IMAGE=node:20.19.4-alpine3.22
ARG NODE_IMAGE_DIGEST
ARG GO_IMAGE=golang:1.25.1-alpine3.22
ARG GO_IMAGE_DIGEST
ARG RUNTIME_IMAGE
ARG RUNTIME_IMAGE_DIGEST

# Digest arguments deliberately have no defaults. Release builds must provide
# immutable digests for the deployment's approved image references.
FROM ${NODE_IMAGE}@sha256:${NODE_IMAGE_DIGEST} AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web/ .
ENV NEXT_TELEMETRY_DISABLED=1
# next.config sets `output: "export"` (prod), so this produces a static site in /web/out.
RUN npm run build

# ---- Stage 2: build the Go backend ----
FROM ${GO_IMAGE}@sha256:${GO_IMAGE_DIGEST} AS go-builder
WORKDIR /src
COPY server/go.mod server/go.sum* ./
RUN go mod download && go mod verify
COPY server/ .
# modernc.org/sqlite is pure Go, so CGO stays off.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -buildid=" -o /moesekai-server .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -buildid=" -o /moesekai-migrate ./cmd/migrate

# ---- Stage 3: runtime (Go only) ----
FROM ${RUNTIME_IMAGE}@sha256:${RUNTIME_IMAGE_DIGEST} AS runtime
ARG VERSION=dev
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="Moesekai Translation" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$VCS_REF \
      org.opencontainers.image.source="https://github.com/StarMoe-org/NEXTmoetrabslation"
# The approved runtime image must already contain pinned git, CA certificates,
# and timezone data; installing mutable packages during release builds is banned.
RUN command -v git >/dev/null && test -s /etc/ssl/certs/ca-certificates.crt
WORKDIR /app

# Backend binaries.
COPY --from=go-builder /moesekai-server ./moesekai-server
COPY --from=go-builder /moesekai-migrate ./moesekai-migrate

# Statically-exported console (served by the Go server at "/").
COPY --from=web-builder /web/out ./web

# Optional seed translations (used on first run when the DB is empty). This repo
# ships no translations/ dir, so the entrypoint detects the absent seed and
# starts with an empty DB; uncomment the COPY below if you add a seed tree.
# COPY translations/ ./seed-translations/
COPY --chmod=755 docker-entrypoint.sh ./docker-entrypoint.sh

# Numeric ownership avoids depending on mutable account files in the approved
# runtime base. Application code is immutable to that UID; only persistent data
# and the base image's explicit temporary directory remain writable.
RUN mkdir -p /data && chown -R 65532:65532 /data && chmod 0700 /data && \
    chown -R 0:0 /app && chmod -R a-w /app

ENV DB_PATH=/data/moesekai.db \
    DATA_DIR=/data \
    WEB_DIR=/app/web \
    HOME=/tmp

VOLUME ["/data"]
# The server listens on $PORT (default 8080; the platform may inject its own).
EXPOSE 8080

USER 65532:65532
CMD ["./docker-entrypoint.sh"]

# ---- Stage 4: immutable backend/workspace pairing (default final image) ----
# Supply the producer artifact as a BuildKit named context:
#   --build-context workspace=/path/to/workspace
FROM runtime AS paired
ARG WORKSPACE_MANIFEST_SHA256
ARG WORKSPACE_ARCHIVE_SHA256
ARG NEXT_REVISION
ARG MOE_REVISION
ARG MOE_TAG
RUN printf '%s\n' "$WORKSPACE_MANIFEST_SHA256" | grep -Eq '^[0-9a-f]{64}$' && \
    printf '%s\n' "$WORKSPACE_ARCHIVE_SHA256" | grep -Eq '^[0-9a-f]{64}$' && \
    printf '%s\n' "$NEXT_REVISION" | grep -Eq '^[0-9a-f]{40}$' && \
    printf '%s\n' "$MOE_REVISION" | grep -Eq '^[0-9a-f]{40}$' && \
    printf '%s\n' "$MOE_TAG" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
LABEL org.opencontainers.image.revision=$NEXT_REVISION \
      io.sekaitext.pair.next.revision=$NEXT_REVISION \
      io.sekaitext.pair.moe.revision=$MOE_REVISION \
      io.sekaitext.pair.moe.tag=$MOE_TAG \
      io.sekaitext.pair.workspace.archive.digest="sha256:${WORKSPACE_ARCHIVE_SHA256}" \
      io.sekaitext.pair.workspace.manifest.digest="sha256:${WORKSPACE_MANIFEST_SHA256}"
USER root
COPY --from=workspace --chown=0:0 --chmod=0555 ./ /app/workspace/
RUN chown -R 0:0 /app && chmod -R a-w /app && chmod 0555 /app /app/workspace
ENV WORKSPACE_WEB_DIR=/app/workspace \
    WORKSPACE_MANIFEST_SHA256=${WORKSPACE_MANIFEST_SHA256} \
    MOESEKAI_PRODUCTION=true
USER 65532:65532
RUN ./moesekai-server --verify-workspace
