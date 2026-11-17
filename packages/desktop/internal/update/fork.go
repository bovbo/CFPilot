package update

// fork 版（你自己仓库构建的桌面版）的更新来源。
//
// 上游的更新流程从上游仓库的 v* 发行版取安装包；fork 版必须从**自己的仓库**取，
// 否则会被「升级」成上游版本（丢掉多数据面等功能）。
//
// 约定（由工作流 kwrt-sync-build.yml 发布到 Release `kwrt-latest`）：
//
//	VERSION-windows                    一行版本号，例如 0.50.23
//	cfpilot-windows-amd64.exe          程序本体
//	SHA256SUMS-desktop                 摘要清单（下载时强制校验，沿用上游的校验流程）
//
// 下载 / 校验 / 替换 / 重启仍然走上游已有的流程，这里只替换「检查有没有新版」这一步。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xmwengxing/cfpilot/packages/desktop/internal/proxy"
)

// ForkBase 非空时启用 fork 更新来源，形如 https://github.com/bovbo/CFPilot/releases。
var ForkBase string

const forkTag = "kwrt-latest"

// UseFork 切换到 fork 更新来源（releasesBase 不带结尾斜杠）。
func UseFork(releasesBase string) {
	ForkBase = strings.TrimRight(strings.TrimSpace(releasesBase), "/")
	if ForkBase != "" {
		ReleasesWebBase = ForkBase
	}
}

func forkCheck(ctx context.Context, current string) *CheckResult {
	res := &CheckResult{Current: current}
	base := fmt.Sprintf("%s/download/%s", ForkBase, forkTag)

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/VERSION-windows", nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "CFPilot-Desktop")
	resp, err := proxy.Resolve().Client(20 * time.Second).Do(req)
	if err != nil {
		res.Error = "无法获取最新版本信息：" + err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("无法获取最新版本信息（HTTP %d）：仓库还没有发布 Windows 版？", resp.StatusCode)
		return res
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	latest := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	if latest == "" {
		res.Error = "最新版本信息为空"
		return res
	}
	res.Latest = latest
	res.DownloadURL = base + "/cfpilot-windows-amd64.exe"
	res.Notes = "来自你自己仓库的发布（含上游最新更新）。"
	// 版本号形如 0.50.23（第三位随提交递增）：只要和当前不一样就算有新版。
	res.HasUpdate = latest != strings.TrimSpace(current)
	return res
}
