#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
target=${1:-}
arch=${2:-$(dpkg --print-architecture)}

case "$target" in
    uos20)
        system=uos
        depends='libc6 (>= 2.28), libgtk-3-0, libwebkit2gtk-4.0-37'
        go_bin=${LOONGSON_GO:-"$HOME/sdk/go1.26.4-abi1.0/bin/go"}
        ;;
    aosc)
        system=aosc
        depends='glibc, gtk-3, webkit2gtk'
        go_bin=${AOSC_GO:-go}
        ;;
    deepin25)
        system=deepin
        depends='libc6 (>= 2.38), libgtk-3-0, libwebkit2gtk-4.0-37'
        go_bin=${DEEPIN_GO:-go}
        ;;
    kylinv10)
        system=kylin
        case "$arch" in
            loongarch64)
                depends='libc6 (>= 2.28), libgtk-3-0, libwebkit2gtk-4.0-37 (>= 2.38.6)'
                go_bin=${LOONGSON_GO:-"$HOME/sdk/go1.26.4-abi1.0/bin/go"}
                ;;
            arm64)
                depends='libc6 (>= 2.31), libgtk-3-0, libwebkit2gtk-4.0-37'
                go_bin=${KYLIN_GO:-go}
                ;;
        esac
        ;;
    *)
        echo "用法：$0 {uos20|aosc|deepin25|kylinv10} [架构]" >&2
        exit 2
        ;;
esac

case "$target:$arch" in
    uos20:loongarch64|aosc:loongarch64|deepin25:loong64|kylinv10:loongarch64) go_arch=loong64 ;;
    deepin25:arm64|kylinv10:arm64) go_arch=arm64 ;;
    deepin25:amd64) go_arch=amd64 ;;
    *)
        echo "不支持的系统和架构：$target / $arch" >&2
        exit 2
        ;;
esac

. /etc/os-release
if [ "${ID:-}" != "$system" ]; then
    echo "当前系统是 ${ID:-unknown}，需要 $system" >&2
    exit 1
fi
if [ "$(dpkg --print-architecture)" != "$arch" ]; then
    echo "当前 dpkg 架构是 $(dpkg --print-architecture)，需要 $arch" >&2
    exit 1
fi
if ! command -v "$go_bin" >/dev/null 2>&1; then
    echo "找不到 Go：$go_bin" >&2
    exit 1
fi

version=${OFDGO_BUILD_VERSION:-$(cat "$repo/debian/upstream-version")}
package_version="$version+$target.${OFDGO_PACKAGE_REVISION:-2}"
output_dir=${OFDGO_DIST_DIR:-"$repo/dist"}
output="$output_dir/ofdgo_${version}_${target}_${arch}.deb"
stage=$(mktemp -d "${TMPDIR:-/tmp}/ofdgo-deb.XXXXXXXX")
build_repo=$repo
legacy_source=
cleanup() {
    rm -r -- "$stage"
    if [ -n "$legacy_source" ]; then rm -r -- "$legacy_source"; fi
}
trap cleanup EXIT
chmod 755 "$stage"

if [ "$target" = kylinv10 ]; then
    legacy_source=$(mktemp -d "${TMPDIR:-/tmp}/ofdgo-webkit.XXXXXXXX")
    git -c safe.directory="$repo" -C "$repo" archive HEAD | tar -xf - -C "$legacy_source"
    (
        cd "$legacy_source"
        GOWORK=off GOTOOLCHAIN=local "$go_bin" run -trimpath ./debian/legacy-webkit
    )
    build_repo=$legacy_source
fi

mkdir -p "$output_dir"
install -d "$stage/DEBIAN" "$stage/usr/bin" "$stage/usr/share/doc/ofdgo"
install -Dm644 "$repo/debian/ofdgo.desktop" "$stage/usr/share/applications/ofdgo.desktop"
install -Dm644 "$repo/debian/ofdgo.xml" "$stage/usr/share/mime/packages/ofdgo.xml"
install -Dm644 "$repo/assets/webui/ofdgo.png" "$stage/usr/share/icons/hicolor/512x512/apps/ofdgo.png"
install -Dm644 "$repo/assets/webui/ofdgo.svg" "$stage/usr/share/icons/hicolor/scalable/apps/ofdgo.svg"
install -Dm644 "$repo/debian/copyright" "$stage/usr/share/doc/ofdgo/copyright"
install -Dm644 "$repo/NOTICE" "$stage/usr/share/doc/ofdgo/NOTICE"
install -Dm644 "$repo/LICENSE" "$stage/usr/share/doc/ofdgo/LICENSE"
install -Dm644 "$repo/debian/legacy-webkit/NOTICE" "$stage/usr/share/doc/ofdgo/legacy-webkit-NOTICE"
install -Dm755 "$repo/debian/postinst" "$stage/DEBIAN/postinst"
install -Dm755 "$repo/debian/postrm" "$stage/DEBIAN/postrm"

(
    cd "$build_repo"
    GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1 GOOS=linux GOARCH="$go_arch" \
        "$go_bin" build -tags guigo_gtk3 -buildvcs=false -o "$stage/usr/bin/ofdgo" -trimpath \
        -ldflags "-s -w -buildid= -X github.com/xiaoqidun/ofdgo/internal/build.Version=$version" \
        ./cmd/webview
)
chmod 755 "$stage/usr/bin/ofdgo"
size=$(du -sk "$stage/usr" | awk '{print $1}')
sed -e "s/@VERSION@/$package_version/g" \
    -e "s/@SIZE@/$size/g" \
    -e "s/@ARCH@/$arch/g" \
    -e "s/@DEPENDS@/$depends/g" \
    "$repo/debian/control.in" > "$stage/DEBIAN/control"

(
    cd "$stage"
    find usr -type f -print0 | LC_ALL=C sort -z | xargs -0 md5sum > DEBIAN/md5sums
)
dpkg-deb --build --root-owner-group "$stage" "$output"
echo "$output"
