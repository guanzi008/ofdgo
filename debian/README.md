# 龙芯 deb 包

`debian/build.sh` 在对应系统里编译桌面程序并打包，成品放在 `dist/`。

| 系统 | 构建命令 | Go |
| --- | --- | --- |
| UOS 20 龙芯版 | `./scripts/build-uos20-loongarch64.sh` | 龙芯社区 Go 1.26.4 |
| AOSC OS 龙芯版 | `./scripts/build-aosc-loongarch64.sh` | AOSC OS 的 Go |
| Deepin 25 龙芯版 | `./scripts/build-deepin25-loong64.sh` | Go 1.26.4 或更新版 |

构建前安装 `gcc`、`pkg-config`、`libgtk-3-dev`（AOSC OS 包名为 `gtk-3`）。默认版本号还需要 `git`。三个包的依赖分别写在 `debian/build.sh` 中。版本号默认取维护提交的父提交；用 `OFDGO_BUILD_VERSION` 可以指定发布版本。`OFDGO_DIST_DIR` 可以指定输出目录。

包内安装 `/usr/bin/ofdgo`、桌面入口、图标及 OFD 文件类型。`Maintainer` 是这个分支的维护者，原作者和许可见包内 `copyright`、`NOTICE` 和 `LICENSE`。
