package server

// Kwrt 路由器版：界面内一键更新。
//
//	GET  /api/kwrt/update/check    查看当前 / 最新版本（调用 cfpilot-ctl check）
//	POST /api/kwrt/update/apply    后台执行升级（cfpilot-ctl upgrade-bg），升级完成后服务会自动重启
//	GET  /api/kwrt/update/status   升级进度（idle / ok / fail + 最近日志）
//
// 只有在路由器上装了 cfpilot-ctl（/usr/bin/cfpilot-ctl）时才可用；
// 其它平台（桌面版等）一律返回 supported=false，界面会隐藏更新卡片。
// 升级本身全部由 cfpilot-ctl 完成（下载 → 校验 SHA256 → 试运行 → 替换 → 重启，失败自动回退）。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// kwrtCtlPath 是路由器上控制脚本的位置；变量方便测试替换。
var kwrtCtlPath = "/usr/bin/cfpilot-ctl"

func kwrtSupported() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	st, err := os.Stat(kwrtCtlPath)
	return err == nil && !st.IsDir()
}

func (s *Server) registerKwrtUpdateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/kwrt/update/check", s.guard(s.handleKwrtCheck))
	mux.HandleFunc("/api/kwrt/update/apply", s.guard(s.handleKwrtApply))
	mux.HandleFunc("/api/kwrt/update/status", s.guard(s.handleKwrtStatus))
}

func kwrtRun(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, kwrtCtlPath, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s *Server) handleKwrtCheck(w http.ResponseWriter, r *http.Request) {
	if !kwrtSupported() {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false})
		return
	}
	out, err := kwrtRun(40*time.Second, "check", "--json")
	// cfpilot-ctl check 最后一行是 JSON
	line := out
	if i := strings.LastIndex(out, "\n"); i >= 0 {
		line = out[i+1:]
	}
	var info map[string]any
	if jerr := json.Unmarshal([]byte(line), &info); jerr != nil {
		msg := "检查更新失败"
		if err != nil {
			msg += "：" + err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{"supported": true, "error": msg, "raw": out})
		return
	}
	info["supported"] = true
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleKwrtApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
		return
	}
	if !kwrtSupported() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "当前不是路由器版，无法一键更新"})
		return
	}
	out, err := kwrtRun(15*time.Second, "upgrade-bg")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "启动升级失败：" + err.Error(), "log": out})
		return
	}
	s.logLevel("已在后台开始一键更新，完成后服务会自动重启", "ok")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true, "log": out})
}

func (s *Server) handleKwrtStatus(w http.ResponseWriter, r *http.Request) {
	if !kwrtSupported() {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false})
		return
	}
	out, _ := kwrtRun(10*time.Second, "upgrade-status")
	lines := strings.Split(out, "\n")
	state := "idle"
	if len(lines) > 0 && lines[0] != "" {
		state = strings.Fields(lines[0] + " ")[0]
	}
	log := ""
	if len(lines) > 1 {
		log = strings.Join(lines[1:], "\n")
	}
	writeJSON(w, http.StatusOK, map[string]any{"supported": true, "state": state, "log": log})
}
