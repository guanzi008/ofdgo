#!/bin/bash
set -e
export TZ=Asia/Shanghai
export Version="$(git show -s --format='%ct-%H' HEAD)"
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -o assets/webui/wasm/ofdgo.wasm -trimpath -ldflags "-s -w -buildid=" ./cmd/webui/wasm.go
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o main -trimpath -ldflags "-s -w -buildid= -X github.com/xiaoqidun/ofdgo/internal/build.Version=$Version" ./cmd/webui/webui.go