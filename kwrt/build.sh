#!/usr/bin/env bash
# 用法：kwrt/build.sh <仓库根目录> <输出目录> [升级地址]
# 作用：在 kwrt-auto 分支（已含全部路由器版功能）上 测试 → 构建 ARMv7 程序 + 安装脚本 + 校验文件。
# 退出码：0 成功；其它 = 测试/构建失败
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SRC="$(cd "$1" && pwd)"; OUT="$(mkdir -p "$2" && cd "$2" && pwd)"
UPURL="${3:-}"
cd "$SRC"

BASEVER="$(node -p "require('./package.json').version")"
SHA="$(git rev-parse --short=7 HEAD)"
# 版本号 = 上游版本的「次版本」+ 递增的第三位，例如 0.50.25，路由器 / 桌面版 / 内核三处完全一致、不带 +编号。
# 第三位 = 上游版本号第三位 + 本分支领先上游的提交数（每次有新提交就 +1，上游发新版也会跟着涨）。
MAJMIN="${BASEVER%.*}"; PATCH0="${BASEVER##*.}"
UPREF=""
for r in refs/remotes/upstream/main refs/heads/main; do
  if git rev-parse -q --verify "$r" >/dev/null 2>&1; then UPREF="$r"; break; fi
done
AHEAD=0
[ -n "$UPREF" ] && AHEAD="$(git rev-list --count "$UPREF..HEAD" 2>/dev/null || echo 0)"
VER="${MAJMIN}.$((PATCH0 + AHEAD))"
echo "构建版本：$VER"

echo "::group::安装依赖"
[ "${SKIP_NPM_CI:-0}" = 1 ] || npm ci --no-audit --no-fund
echo "::endgroup::"

echo "::group::内核：类型检查 + 测试 + 构建 + 脱敏检查"
npm run typecheck -w @cfpilot/worker-core
npx vitest run packages/worker-core
npm run build -w @cfpilot/worker-core
node scripts/check-kernel-desensitize.mjs
echo "::endgroup::"

echo "::group::界面与桌面资源"
# 上游的资源脚本要求有 go-winres（只为 Windows 图标，路由器版用不上，但脚本强制要求）
export PATH="$PATH:$(go env GOPATH)/bin"
command -v go-winres >/dev/null 2>&1 || go install github.com/tc-hib/go-winres@v0.3.3
npm run build -w @cfpilot/ui
node scripts/build-desktop.mjs --assets-only
echo "::endgroup::"

echo "::group::Go 检查"
cd packages/desktop
test -z "$(gofmt -l ./cmd ./internal)" || { gofmt -l ./cmd ./internal; echo "gofmt 不通过"; exit 1; }
go vet ./...
# 全量 Go 测试（含上游自己的）；联网类测试在 CI 里可以正常跑
go test ./... ${GO_TEST_FLAGS:-}
echo "::endgroup::"

echo "::group::构建 ARMv7"
LDF="-s -w -X main.Version=${VER}"
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "$LDF" -o "$OUT/cfpilot-kwrt-armv7" ./cmd/cfpilot-kwrt
# 冒烟：同源码编 amd64 版跑一下 --version，确认程序能启动
CGO_ENABLED=0 go build -trimpath -ldflags "$LDF" -o "$OUT/.smoke" ./cmd/cfpilot-kwrt
"$OUT/.smoke" --version | tee "$OUT/.smoke.txt"; grep -q "$VER" "$OUT/.smoke.txt"
rm -f "$OUT/.smoke" "$OUT/.smoke.txt"
# ARM 头校验：ELF、32 位、EM_ARM(0x28)
head -c 20 "$OUT/cfpilot-kwrt-armv7" | od -An -tx1 | tr -d ' \n' | grep -q '^7f454c4601.*2800' || { echo "产物不是 ARM ELF"; exit 1; }
echo "::endgroup::"

echo "::group::构建 Windows 绿色版（桌面版，带多数据面页）"
WINDIR_NAME="代理微内核面板"
# 更新来源 = 本仓库的 Releases 页（由 ARMv7 的升级地址推出；本地测试没给地址就留空）
UPBASE=""
[ -n "$UPURL" ] && UPBASE="${UPURL%/download/*}"
# exe 图标与版本属性：尽力而为（生成失败 / 空文件就不带图标，不影响使用）
SYSO="cmd/cfpilot/rsrc_windows_amd64.syso"
rm -f "$SYSO"
if command -v go-winres >/dev/null 2>&1 && [ -f build/app.ico ]; then
  ( cd build && go-winres simply --icon app.ico --product-name CFPilot \
      --file-description "CFPilot 部署与维护工具（多数据面版）" --copyright "Copyright © CFPilot Contributors" \
      --original-filename CFPilot.exe --file-version "$BASEVER" --product-version "$BASEVER" \
      --manifest gui --out ../cmd/cfpilot/rsrc ) || echo "（图标资源生成失败，继续）"
fi
[ -s "$SYSO" ] || rm -f "$SYSO"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -H=windowsgui -X main.Version=${VER} -X main.UpdateBase=${UPBASE}" \
  -o "$OUT/cfpilot-windows-amd64.exe" ./cmd/cfpilot
rm -f "$SYSO"
head -c 2 "$OUT/cfpilot-windows-amd64.exe" | grep -q 'MZ' || { echo "产物不是 Windows 可执行文件"; exit 1; }
# 绿色版压缩包：解压到哪里都能直接用（数据在 data\、升级包在 updates\，程序首次运行自动创建）
PK="$OUT/.pack/$WINDIR_NAME"
mkdir -p "$PK"
cp "$OUT/cfpilot-windows-amd64.exe" "$PK/CFPilot.exe"
cp "$HERE/windows/使用说明.txt" "$PK/使用说明.txt"
( cd "$OUT/.pack" && zip -q -r -9 "$OUT/CFPilot-windows-portable.zip" "$WINDIR_NAME" )
rm -rf "$OUT/.pack"
cd "$OUT"
# 一键更新用的摘要清单与版本文件（沿用上游的强制校验流程）
echo "$(sha256sum cfpilot-windows-amd64.exe | cut -d' ' -f1)  cfpilot-windows-amd64.exe" > cfpilot-desktop-SHA256SUMS
echo "$VER" > VERSION-windows
cd "$SRC"
echo "::endgroup::"

echo "::group::生成安装脚本与校验"
python3 "$HERE/installer/build.py" "$OUT/install-cfpilot-kwrt.sh" "$UPURL"
sh -n "$OUT/install-cfpilot-kwrt.sh"
cd "$OUT"
sha256sum cfpilot-kwrt-armv7 > cfpilot-kwrt-armv7.sha256
# 压缩版：路由器闪存/流量紧张，优先下载 .gz（解压后仍按 .sha256 校验原文件）
gzip -9 -n -c cfpilot-kwrt-armv7 > cfpilot-kwrt-armv7.gz
gzip -t cfpilot-kwrt-armv7.gz
sha256sum cfpilot-kwrt-armv7 install-cfpilot-kwrt.sh > SHA256SUMS
echo "$VER" > VERSION
git -C "$SRC" rev-parse HEAD > BUILD_SHA
echo "::endgroup::"
ls -l "$OUT"
