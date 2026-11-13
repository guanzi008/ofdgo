# deb 包

`debian/build.sh` 在对应系统里编译桌面程序并打包，成品放在 `dist/`。

| 系统 | 构建命令 | Go |
| --- | --- | --- |
| UOS 20 龙芯版 | `./scripts/build-uos20-loongarch64.sh` | 龙芯社区 Go 1.26.4 |
| UOS 20 ARM64 | `./scripts/build-uos20-arm64.sh` | Go 1.26.4 或更新版 |
| AOSC OS 龙芯版 | `./scripts/build-aosc-loongarch64.sh` | AOSC OS 的 Go |
| Deepin 25 龙芯版 | `./scripts/build-deepin25-loong64.sh` | Go 1.26.4 或更新版 |
| Deepin 25 ARM64 | `./scripts/build-deepin25-arm64.sh` | Go 1.26.4 或更新版 |
| Deepin 25 amd64 | `./scripts/build-deepin25-amd64.sh` | Go 1.26.4 或更新版 |
| 麒麟 V10 ARM64 | `./scripts/build-kylinv10-arm64.sh` | Go 1.26.4 或更新版 |
| 麒麟 V10 龙芯版 | `./scripts/build-kylinv10-loongarch64.sh` | 龙芯社区 Go 1.26.4 |

构建前安装 `gcc`、`pkg-config`、`libgtk-3-dev`（AOSC OS 包名为 `gtk-3`）和 `git`。构建脚本会核对系统、架构，并把依赖写入 deb。上游版本记在 `debian/upstream-version`，同步源码时一起更新；用 `OFDGO_BUILD_VERSION` 可以指定发布版本。`OFDGO_DIST_DIR` 可以指定输出目录。

UOS 20 ARM64 使用 glibc 2.28 构建，也可使用 Debian 10 ARM64 作为兼容构建环境。

麒麟 V10、UOS 20 ARM64 构建时会在临时源码目录中转换前端语法，兼容脚本只在浏览器缺少所需 API 时加载。麒麟龙芯包需要 2403 更新中的 WebKit 2.38.6 或更新版。兼容脚本及生成工具放在 `debian/legacy-webkit/`；重新生成时运行 `npm ci --ignore-scripts` 和 `node build-runtime.cjs`。

麒麟 ARM64 包默认使用软件合成，避免部分驱动下的启动白屏。需要启用合成时可运行 `WEBKIT_DISABLE_COMPOSITING_MODE=0 ofdgo`。

包内安装 `/usr/bin/ofdgo`、桌面入口、图标及 OFD 文件类型。`Maintainer` 是这个分支的维护者，原作者和许可见包内 `copyright`、`NOTICE` 和 `LICENSE`。
