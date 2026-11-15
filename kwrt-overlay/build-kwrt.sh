#!/usr/bin/env bash
# 用法：build-kwrt.sh <上游源码目录(干净检出)> <输出目录> [升级地址]
# 作用：把"我的全部功能补丁"叠到上游最新源码上 → 测试 → 构建 ARMv7 程序 + 安装脚本 + 校验文件。
# 退出码：0 成功；2 补丁与上游冲突（需要人工重新对齐）；其它 = 测试/构建失败
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SRC="$(cd "$1" && pwd)"; OUT="$(mkdir -p "$2" && cd "$2" && pwd)"
UPURL="${3:-}"
cd "$SRC"

echo "::group::应用功能补丁"
# 先严格检查；不行再允许三方合并（上游改了相邻行时通常能自动合并，之后仍由测试把关）
if git apply --check "$HERE/kwrt-1n.patch" 2>/dev/null; then
  git apply "$HERE/kwrt-1n.patch"
elif git apply --3way "$HERE/kwrt-1n.patch"; then
  if git diff --name-only --diff-filter=U | grep -q .; then echo "补丁出现冲突文件："; git diff --name-only --diff-filter=U; exit 2; fi
  echo "补丁通过三方合并应用（上游相邻代码有变动，已由合并处理，下面测试会把关）"
else
  echo "补丁与上游最新代码冲突，需要重新对齐"; exit 2
fi
echo "::endgroup::"

BASEVER="$(node -p "require('./package.json').version")"
SHA="$(git rev-parse --short=7 HEAD)"
VER="${BASEVER}+${SHA}"
echo "上游版本：$VER"

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

echo "::group::生成安装脚本与校验"
python3 "$HERE/installer/build.py" "$OUT/install-cfpilot-kwrt.sh" "$UPURL"
sh -n "$OUT/install-cfpilot-kwrt.sh"
cd "$OUT"
sha256sum cfpilot-kwrt-armv7 > cfpilot-kwrt-armv7.sha256
sha256sum cfpilot-kwrt-armv7 install-cfpilot-kwrt.sh > SHA256SUMS
echo "$VER" > VERSION
echo "$(git -C "$SRC" rev-parse HEAD)" > UPSTREAM_SHA
echo "::endgroup::"
ls -l "$OUT"
