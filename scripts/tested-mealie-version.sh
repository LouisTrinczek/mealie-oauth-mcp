#!/bin/sh
set -eu
version=$(sed -n 's/.*MEALIE_VERSION:-\(v[0-9.]*\)@.*/\1/p' test/integration/compose.yml)
if [ -z "$version" ]; then
  echo "no pinned MEALIE_VERSION in test/integration/compose.yml" >&2
  exit 1
fi
echo "$version"
