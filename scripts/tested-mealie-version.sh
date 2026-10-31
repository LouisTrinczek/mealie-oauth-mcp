#!/bin/sh
set -eu
sed -n 's/.*MEALIE_VERSION:-\(v[0-9.]*\)@.*/\1/p' test/integration/compose.yml
