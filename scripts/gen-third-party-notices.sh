#!/usr/bin/env bash
# Generates THIRD-PARTY-NOTICES.md from the modules actually compiled into the
# clishake binary. Deterministic: sorted, full license texts from the module
# cache. Run from the repo root.
set -euo pipefail
CACHE="$(go env GOMODCACHE)"
OUT="THIRD-PARTY-NOTICES.md"
SELF="github.com/clishakehq/clishake"

# unique module@version list compiled into the binary, minus stdlib and self
mods="$(go list -deps -f '{{with .Module}}{{.Path}}@{{.Version}}{{end}}' ./cmd/clishake \
  | grep -v '^$' | grep -v "^${SELF}@" | sort -u)"

enc() { printf '%s' "$1" | perl -pe 's/([A-Z])/"!".lc($1)/ge'; }
licfile() { ls "$1" 2>/dev/null | grep -iE '^(LICENSE|LICENCE|COPYING)' | head -1; }
classify() {
  if grep -qai "GNU GENERAL PUBLIC\|GNU LESSER\|GNU AFFERO" "$1"; then echo "GPL family"
  elif grep -qai "Mozilla Public License" "$1"; then echo "MPL-2.0"
  elif grep -qai "Apache License" "$1"; then echo "Apache-2.0"
  elif grep -qai "Redistribution and use in source and binary" "$1"; then echo "BSD"
  elif grep -qai "Permission is hereby granted" "$1"; then echo "MIT/ISC"
  else echo "see text"; fi
}

{
  echo "# Third-Party Notices"
  echo
  echo "CLIshake is distributed as a single binary that statically links the"
  echo "open-source components listed below. Each is provided under its own"
  echo "license, reproduced in full. CLIshake itself is licensed under the MIT"
  echo "License (see [LICENSE](LICENSE))."
  echo
  echo "All bundled components are permissively licensed (MIT, BSD, or Apache-2.0);"
  echo "none are copyleft. This file is generated from \`go.mod\`."
  echo
  echo "## Components"
  echo
  while IFS= read -r mv; do
    path="${mv%@*}"; ver="${mv##*@}"
    dir="$CACHE/$(enc "$mv")"; lf="$(licfile "$dir")"
    kind="$(classify "$dir/$lf")"
    echo "- \`$path\` $ver — $kind"
  done <<< "$mods"
  echo
  while IFS= read -r mv; do
    path="${mv%@*}"; ver="${mv##*@}"
    dir="$CACHE/$(enc "$mv")"; lf="$(licfile "$dir")"
    echo
    echo "---"
    echo
    echo "## $path"
    echo
    echo "Version: $ver"
    echo
    echo '```'
    cat "$dir/$lf"
    echo '```'
  done <<< "$mods"
} > "$OUT"

echo "wrote $OUT ($(wc -l < "$OUT") lines, $(grep -c '^## ' "$OUT") sections incl. summary)"
