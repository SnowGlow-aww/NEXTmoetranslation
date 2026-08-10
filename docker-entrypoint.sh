#!/bin/sh
set -eu

DB_PATH="${DB_PATH:-/data/moesekai.db}"
DATA_DIR="${DATA_DIR:-/data}"
SEED_DIR="/app/seed-translations"
PORT="${PORT:-8080}"

echo "=== MOESEKAI v2 STARTUP ==="
echo "DB_PATH:  $DB_PATH"
echo "DATA_DIR: $DATA_DIR"
echo "PORT:     $PORT (Go: serves console SPA + /api + /sse + /files)"

# Validate the server-runtime workspace policy before touching the persistent
# data directory or attempting any seed migration. The Go process repeats this
# check immediately before normal startup.
./moesekai-server --verify-runtime

umask 077
mkdir -p "$DATA_DIR"
chmod 0700 "$DATA_DIR"
if [ -f "$DB_PATH" ]; then
  chmod 0600 "$DB_PATH"
fi

if [ -f "$DB_PATH" ] && [ -e "$DB_PATH.seed-incomplete" ]; then
  echo "Fatal: database has an incomplete seed publication marker: $DB_PATH.seed-incomplete" >&2
  exit 1
fi

# On first run (no DB yet), seed translations from the image into SQLite.
if [ ! -f "$DB_PATH" ] && [ -d "$SEED_DIR" ]; then
  echo "No database found; migrating seed translations into SQLite..."
  ./moesekai-migrate -src "$SEED_DIR" -db "$DB_PATH"
else
  echo "Database present or no seed; skipping migration."
fi

# The Go server is the only process: it serves the static console and the API on
# one port. exec replaces the shell so signals reach Go directly (clean shutdown)
# and its tagged, timestamped logs go straight to `docker logs`.
echo "Starting moesekai-server on :${PORT}..."
exec ./moesekai-server
