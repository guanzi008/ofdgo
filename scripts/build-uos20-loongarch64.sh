#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec "$repo/debian/build.sh" uos20 "$@"
