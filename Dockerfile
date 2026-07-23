# Build from the REPOSITORY ROOT (this directory):
#
#   docker build \
#     --build-arg NODE_IMAGE=node:20.19.4-alpine3.22@sha256:<approved-digest> \
#     --build-arg GO_IMAGE=golang:1.25.1-alpine3.22@sha256:<approved-digest> \
#     --build-arg RUNTIME_IMAGE=<approved-runtime-with-git>@sha256:<approved-digest> \
#     --build-arg VERSION=<release> --build-arg VCS_REF=$(git rev-parse HEAD) \
#     -t moesekai-v2 .
#
# A .dockerignore keeps host-built web/node_modules and web/.next out of the
# context so they cannot clobber the Linux artifacts produced in the builders.
#
# Architecture: ONE process. The Go backend serves the statically-exported
# console SPA at "/" and the API/SSE/files at /api, /sse, /files. No nginx, no
# Node.js at runtime.

ARG NODE_IMAGE
ARG GO_IMAGE
ARG RUNTIME_IMAGE

# Build arguments deliberately have no mutable defaults. Release builds must
# provide digest-qualified base images from the deployment's approved registry.
FROM ${NODE_IMAGE} AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web/ .
ENV NEXT_TELEMETRY_DISABLED=1
# next.config sets `output: "export"` (prod), so this produces a static site in /web/out.
RUN npm run build

# ---- Stage 2: build the Go backend ----
FROM ${GO_IMAGE} AS go-builder
WORKDIR /src
COPY server/go.mod server/go.sum* ./
RUN go mod download && go mod verify
COPY server/ .
# modernc.org/sqlite is pure Go, so CGO stays off.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -buildid=" -o /moesekai-server .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -buildid=" -o /moesekai-migrate ./cmd/migrate

# ---- Stage 3: runtime (Go only) ----
FROM ${RUNTIME_IMAGE} AS runtime
ARG VERSION=dev
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="Moesekai Translation" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$VCS_REF \
      org.opencontainers.image.source="https://github.com/SnowGlow-aww/production-next-locale-lyrics"
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

ENV DB_PATH=/data/moesekai.db \
    DATA_DIR=/data \
    WEB_DIR=/app/web

VOLUME ["/data"]
# The server listens on $PORT (default 8080; the platform may inject its own).
EXPOSE 8080

CMD ["./docker-entrypoint.sh"]
