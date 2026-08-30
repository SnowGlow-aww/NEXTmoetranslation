#!/bin/sh
set -eu

if [ "$#" -ne 2 ] || [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
  echo "usage: SOURCE_DATE_EPOCH=<unix-seconds> $0 <staging-root> <output.tar>" >&2
  exit 2
fi

root=$1
output=$2
case "$SOURCE_DATE_EPOCH" in
  ''|*[!0-9]*) echo "SOURCE_DATE_EPOCH must be a nonnegative integer" >&2; exit 2 ;;
esac
test -f "$root/moesekai-server"
test -f "$root/moesekai-migrate"
test -d "$root/web"
tar --version 2>/dev/null | grep -q 'GNU tar'

# Normalize the complete archive mode surface, then restore executable modes for
# the two entrypoints. These modes are represented directly in the tar headers.
find "$root" -type d -exec chmod 0755 {} +
find "$root" -type f -exec chmod 0644 {} +
chmod 0755 "$root/moesekai-server" "$root/moesekai-migrate"

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

(
  cd "$root"
  find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort | while IFS= read -r file; do
    digest=$(hash_file "$file")
    printf '%s  %s\n' "$digest" "${file#./}"
  done
) > "$root/SHA256SUMS"
chmod 0644 "$root/SHA256SUMS"

output_dir=$(dirname "$output")
output_name=$(basename "$output")
output_dir=$(cd "$output_dir" && pwd)
temporary="$output_dir/.$output_name.tmp"
trap 'rm -f "$temporary"' EXIT HUP INT TERM
LC_ALL=C tar --sort=name --format=gnu --mtime="@$SOURCE_DATE_EPOCH" \
  --owner=0 --group=0 --numeric-owner -cf "$temporary" -C "$root" .
mv "$temporary" "$output_dir/$output_name"
trap - EXIT HUP INT TERM
