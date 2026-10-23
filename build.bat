setlocal
set Version=
for /f %%V in ('git show -s --format^=%%ct-%%H HEAD') do set Version=%%V

set GOOS=linux
set GOARCH=arm64
set CGO_ENABLED=0
go build -o releases/ofdgo_linux_arm64 -trimpath -ldflags "-s -w -buildid= -X github.com/xiaoqidun/ofdgo/internal/build.Version=%Version%" ./cmd/webview

set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
go build -o releases/ofdgo_linux_amd64 -trimpath -ldflags "-s -w -buildid= -X github.com/xiaoqidun/ofdgo/internal/build.Version=%Version%" ./cmd/webview

set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -o releases/ofdgo_windows_amd64.exe -trimpath -ldflags "-H windowsgui -s -w -buildid= -X github.com/xiaoqidun/ofdgo/internal/build.Version=%Version%" ./cmd/webview