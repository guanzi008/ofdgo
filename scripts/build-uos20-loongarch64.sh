#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
go_bin=${LOONGSON_GO:-"$HOME/sdk/go1.26.4-abi1.0/bin/go"}
output=${OFDGO_OUTPUT:-"$repo/ofdgo_uos20_loongarch64"}

if [ ! -x "$go_bin" ]; then
    echo "找不到龙芯社区 Go：$go_bin" >&2
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
