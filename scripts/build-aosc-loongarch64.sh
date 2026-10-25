#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
go_bin=${AOSC_GO:-go}
output=${OFDGO_OUTPUT:-"$repo/ofdgo_aosc_loongarch64"}

if [ "$(uname -m)" != loongarch64 ]; then
    echo "请在 AOSC OS 龙芯环境中运行" >&2
    exit 1
fi
if ! command -v "$go_bin" >/dev/null 2>&1; then
    echo "找不到 Go：$go_bin" >&2
    exit 1
fi

version=${OFDGO_BUILD_VERSION:-}
if [ -z "$version" ]; then
    version=$(git -C "$repo" show -s --format=%cd-%h --date=format:%Y%m%d HEAD 2>/dev/null || true)
fi
if [ -z "$version" ]; then
    version=local
fi

(
    cd "$repo"
    GOWORK=off CGO_ENABLED=1 "$go_bin" build \
        -o "$output" -trimpath \
        -ldflags "-s -w -buildid= -X github.com/xiaoqidun/ofdgo/internal/build.Version=$version" \
        ./cmd/webview
)
