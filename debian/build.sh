#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
target=${1:-}

case "$target" in
    uos20)
        system=uos
        arch=loongarch64
        depends='libc6 (>= 2.28), libgtk-3-0, libwebkit2gtk-4.0-37'
        go_bin=${LOONGSON_GO:-"$HOME/sdk/go1.26.4-abi1.0/bin/go"}
        ;;
    aosc)
        system=aosc
        arch=loongarch64
        depends='glibc, gtk-3, webkit2gtk'
        go_bin=${AOSC_GO:-go}
        ;;
    deepin25)
        system=deepin
        arch=loong64
        depends='libc6 (>= 2.38), libgtk-3-0, libwebkit2gtk-4.0-37'
        go_bin=${DEEPIN_GO:-go}
        ;;
    *)
        echo "用法：$0 {uos20|aosc|deepin25}" >&2
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

version=${OFDGO_BUILD_VERSION:-$(git -C "$repo" show -s --format=%cd-%h --date=format:%Y%m%d HEAD^)}
package_version="$version+$target.${OFDGO_PACKAGE_REVISION:-1}"
output_dir=${OFDGO_DIST_DIR:-"$repo/dist"}
output="$output_dir/ofdgo_${version}_${target}_${arch}.deb"
stage=$(mktemp -d "${TMPDIR:-/tmp}/ofdgo-deb.XXXXXXXX")
trap 'rm -r -- "$stage"' EXIT
chmod 755 "$stage"

mkdir -p "$output_dir"
install -d "$stage/DEBIAN" "$stage/usr/bin" "$stage/usr/share/doc/ofdgo"
install -Dm644 "$repo/debian/ofdgo.desktop" "$stage/usr/share/applications/ofdgo.desktop"
install -Dm644 "$repo/debian/ofdgo.xml" "$stage/usr/share/mime/packages/ofdgo.xml"
install -Dm644 "$repo/assets/webui/ofdgo.png" "$stage/usr/share/icons/hicolor/512x512/apps/ofdgo.png"
install -Dm644 "$repo/assets/webui/ofdgo.svg" "$stage/usr/share/icons/hicolor/scalable/apps/ofdgo.svg"
install -Dm644 "$repo/debian/copyright" "$stage/usr/share/doc/ofdgo/copyright"
install -Dm644 "$repo/NOTICE" "$stage/usr/share/doc/ofdgo/NOTICE"
install -Dm644 "$repo/LICENSE" "$stage/usr/share/doc/ofdgo/LICENSE"
install -Dm755 "$repo/debian/postinst" "$stage/DEBIAN/postinst"
install -Dm755 "$repo/debian/postrm" "$stage/DEBIAN/postrm"

(
    cd "$repo"
    GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1 GOOS=linux GOARCH=loong64 \
        "$go_bin" build -buildvcs=false -o "$stage/usr/bin/ofdgo" -trimpath \
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
