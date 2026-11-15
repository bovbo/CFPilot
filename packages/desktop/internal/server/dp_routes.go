package server

// 1:N 多数据面管理接口。
//
//	GET  /api/dp                 数据面清单 + 全局设置 + 账号列表 + 各数据面在控制面的池条数
//	POST /api/dp/save            新增 / 更新一个数据面（新增且开启「新增后推送」时自动推送）
//	POST /api/dp/delete          删除一个数据面（同步到控制面，独立池一并清掉）
//	POST /api/dp/sync-deploy     从该数据面所属账号的部署清单读取域名 / UUID / 路径 / 内核版本
//	POST /api/dp/push            手动推送（异步；结果见各数据面的推送记录）
//	POST /api/dp/ips/remove      从某个数据面的池里删除指定 IP
//	POST /api/dp/settings        保存全局推送设置
//	POST /api/dp/deploy          各账号独立部署 / 升级数据面（仅数据面，逐个串行，见 dp_deploy.go）

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xmwengxing/cfpilot/packages/desktop/internal/deploy"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/dpreg"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/scan"
)

func (s *Server) registerDPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/dp", s.guard(s.handleDPList))
	mux.HandleFunc("/api/dp/save", s.guard(s.handleDPSave))
	mux.HandleFunc("/api/dp/delete", s.guard(s.handleDPDelete))
	mux.HandleFunc("/api/dp/sync-deploy", s.guard(s.handleDPSyncDeploy))
	mux.HandleFunc("/api/dp/push", s.guard(s.handleDPPush))
	mux.HandleFunc("/api/dp/ips/remove", s.guard(s.handleDPRemoveIPs))
	mux.HandleFunc("/api/dp/settings", s.guard(s.handleDPSettings))
	mux.HandleFunc("/api/dp/deploy", s.guard(s.handleDPDeploy))
}

func dpDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体解析失败"})
		return false
	}
	return true
}

// handleDPList 返回数据面清单。
func (s *Server) handleDPList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET"})
		return
	}
	st := s.dpStore()
	type profileRow struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		AccountName string `json:"accountName"`
		HasToken    bool   `json:"hasToken"`
		HasKV       bool   `json:"hasKv"`
	}
	var profiles []profileRow
	if list, err := s.store.List(); err == nil {
		for _, p := range list {
			profiles = append(profiles, profileRow{
				ID: p.ID, Name: p.Name, AccountName: p.AccountName,
				HasToken: strings.TrimSpace(p.CfToken) != "", HasKV: strings.TrimSpace(p.KVNamespaceID) != "",
			})
		}
	}
	pools := map[string]int{}
	poolErr := ""
	if r.URL.Query().Get("pools") == "1" && len(st.List()) > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if host, pw, err := s.dpControl(ctx); err != nil {
			poolErr = err.Error()
		} else if tok := scan.FetchScannerToken(ctx, host, pw, 20*time.Second); !tok.OK {
			poolErr = tok.Error
		} else if m, err := scan.PoolCounts(ctx, host, tok.Token, 15*time.Second); err != nil {
			poolErr = err.Error()
		} else {
			pools = m
		}
	}
	dpPushing.Lock()
	pushing := dpPushing.on
	dpPushing.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "planes": st.List(), "settings": st.Settings(), "profiles": profiles,
		"pools": pools, "poolError": poolErr, "pushing": pushing,
		"fixedDomains": st.FixedRecords(),
	})
}

type dpSaveReq struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ProfileID      string `json:"profileId"`
	WorkerName     string `json:"workerName"`
	Domain         string `json:"domain"`
	UUID           string `json:"uuid"`
	WSPath         string `json:"wsPath"`
	TrojanPassword string `json:"trojanPassword"`
	TrojanPath     string `json:"trojanPath"`
	XHTTPPath      string `json:"xhttpPath"`
	Enabled        *bool  `json:"enabled"`
	AutoPush       *bool  `json:"autoPush"`
	// FromDeploy 为 true 时先从账号的部署清单读取域名 / UUID / 路径（留空的字段才会被覆盖）。
	FromDeploy bool `json:"fromDeploy"`
}

// dpReadDeploy 读该账号的部署清单 + KV CONFIG，填充 dp 里还没填的字段。
func (s *Server) dpReadDeploy(ctx context.Context, dp *dpreg.DataPlane, overwrite bool) error {
	profile, err := s.store.Get(dp.ProfileID)
	if err != nil || profile == nil {
		return fmt.Errorf("所属账号不存在")
	}
	if strings.TrimSpace(profile.CfToken) == "" || strings.TrimSpace(profile.AccountID) == "" || strings.TrimSpace(profile.KVNamespaceID) == "" {
		return fmt.Errorf("该账号缺少 API Token / Account ID / KV，请先在凭据页检测账号并选择 KV")
	}
	rctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client := s.newCFClient(profile.CfToken)
	raw, ok := client.GetKVValue(rctx, profile.AccountID, profile.KVNamespaceID, deploy.ManifestKey)
	if !ok || strings.TrimSpace(raw) == "" {
		return fmt.Errorf("该账号还没有部署清单，请先在部署页完成一次部署")
	}
	var m deploy.Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return fmt.Errorf("部署清单格式损坏")
	}
	set := func(dst *string, v string) {
		v = strings.TrimSpace(v)
		if v != "" && (overwrite || strings.TrimSpace(*dst) == "") {
			*dst = v
		}
	}
	domain := strings.TrimSpace(m.DataCustomDomain)
	if domain == "" {
		domain = normalizePagesHost(m.DataSubdomain)
	}
	set(&dp.Domain, domain)
	set(&dp.UUID, m.Credentials.UUID)
	set(&dp.TrojanPassword, m.Credentials.TrojanPassword)
	set(&dp.WSPath, m.Credentials.WSPath)
	set(&dp.WorkerName, m.DataProject)
	dp.KernelVersion = strings.TrimSpace(m.KernelVersion)
	dp.LastDeployAt = strings.TrimSpace(m.At)
	// KV 里的 CONFIG 才是现行配置（清单里的路径只是部署当时的快照）
	if rawCfg, ok := client.GetKVValue(rctx, profile.AccountID, profile.KVNamespaceID, deploy.ConfigKey); ok && rawCfg != "" {
		var cfg struct {
			WsPath         string `json:"wsPath"`
			XhttpPath      string `json:"xhttpPath"`
			TrojanPath     string `json:"trojanPath"`
			TrojanPassword string `json:"trojanPassword"`
			UUID           string `json:"uuid"`
		}
		if json.Unmarshal([]byte(rawCfg), &cfg) == nil {
			set(&dp.WSPath, cfg.WsPath)
			set(&dp.XHTTPPath, cfg.XhttpPath)
			set(&dp.TrojanPath, cfg.TrojanPath)
			set(&dp.TrojanPassword, cfg.TrojanPassword)
			set(&dp.UUID, cfg.UUID)
		}
	}
	return nil
}

func (s *Server) handleDPSave(w http.ResponseWriter, r *http.Request) {
	var req dpSaveReq
	if !dpDecode(w, r, &req) {
		return
	}
	st := s.dpStore()
	isNew := strings.TrimSpace(req.ID) == ""
	dp := dpreg.DataPlane{Enabled: true, AutoPush: true}
	if !isNew {
		cur, ok := st.Get(req.ID)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "数据面不存在"})
			return
		}
		dp = cur
	} else {
		dp.ID = dpreg.NewID()
	}
	if req.ProfileID != "" {
		dp.ProfileID = req.ProfileID
	}
	if strings.TrimSpace(req.Name) != "" {
		dp.Name = req.Name
	}
	dp.WorkerName = firstNonEmpty(req.WorkerName, dp.WorkerName)
	dp.Domain = firstNonEmpty(req.Domain, dp.Domain)
	dp.UUID = firstNonEmpty(req.UUID, dp.UUID)
	dp.WSPath = firstNonEmpty(req.WSPath, dp.WSPath)
	dp.TrojanPassword = firstNonEmpty(req.TrojanPassword, dp.TrojanPassword)
	dp.TrojanPath = firstNonEmpty(req.TrojanPath, dp.TrojanPath)
	dp.XHTTPPath = firstNonEmpty(req.XHTTPPath, dp.XHTTPPath)
	if req.Enabled != nil {
		dp.Enabled = *req.Enabled
	}
	if req.AutoPush != nil {
		dp.AutoPush = *req.AutoPush
	}
	if p, err := s.store.Get(dp.ProfileID); err != nil || p == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "请选择该数据面所属的账号（账号需先在凭据页添加）"})
		return
	}
	if req.FromDeploy {
		if err := s.dpReadDeploy(r.Context(), &dp, false); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "从部署读取失败：" + err.Error()})
			return
		}
	}
	saved, err := st.Upsert(dp)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// 同步到控制面：订阅聚合立刻生效（池为空时先借用全局池，新数据面马上有节点）。
	warn := ""
	sctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.dpSyncRegistry(sctx); err != nil {
		warn = "已保存到本地，但同步控制面失败（下次推送时会重试）：" + err.Error()
	}

	started := false
	if isNew && warn == "" && st.Settings().PushAfterAdd && saved.Enabled {
		if lines := s.dpSourceLines(st.Settings().Top); len(lines) > 0 {
			started = true
			id := saved.ID
			go func() {
				res, err := s.dpPushAll(context.Background(), []string{id}, lines, st.Settings().Mode, "add", false)
				if err != nil {
					s.logLevel("新增数据面后推送未执行："+err.Error(), "warn")
					return
				}
				s.dpLogResults("新增数据面推送", res)
			}()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plane": saved, "warning": warn, "pushStarted": started})
}

func (s *Server) handleDPDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !dpDecode(w, r, &req) {
		return
	}
	ok, err := s.dpStore().Delete(req.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "数据面不存在"})
		return
	}
	warn := ""
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.dpSyncRegistry(ctx); err != nil {
		warn = "本地已删除，但同步控制面失败（控制面上的订阅暂时还含它，下次同步会清掉）：" + err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": warn})
}

func (s *Server) handleDPSyncDeploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !dpDecode(w, r, &req) {
		return
	}
	dp, ok := s.dpStore().Get(req.ID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "数据面不存在"})
		return
	}
	if err := s.dpReadDeploy(r.Context(), &dp, true); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	saved, err := s.dpStore().Upsert(dp)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	warn := ""
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.dpSyncRegistry(ctx); err != nil {
		warn = "已更新本地，但同步控制面失败：" + err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plane": saved, "warning": warn})
}

func (s *Server) handleDPPush(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs   []string `json:"ids"`
		Mode  string   `json:"mode"`
		Lines []string `json:"lines"`
	}
	if !dpDecode(w, r, &req) {
		return
	}
	st := s.dpStore()
	set := st.Settings()
	mode := req.Mode
	if mode != "append" && mode != "replace" {
		mode = set.Mode
	}
	lines := req.Lines
	if len(lines) == 0 {
		lines = s.dpSourceLines(set.Top)
	}
	if len(lines) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "还没有可推送的优选结果：先跑一次优选（或在输入框里粘贴 IP 行）"})
		return
	}
	if len(lines) > 200 {
		lines = lines[:200]
	}
	if !dpPushLock.TryLock() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "已有一轮推送正在执行，请稍后再试"})
		return
	}
	dpPushLock.Unlock()
	go func() {
		res, err := s.dpPushAll(context.Background(), req.IDs, lines, mode, "manual", false)
		if err != nil {
			s.logLevel("手动推送未执行："+err.Error(), "warn")
			return
		}
		s.dpLogResults("手动推送", res)
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": true, "mode": mode, "count": len(lines)})
}

func (s *Server) handleDPRemoveIPs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID  string   `json:"id"`
		IPs []string `json:"ips"`
	}
	if !dpDecode(w, r, &req) {
		return
	}
	dp, ok := s.dpStore().Get(req.ID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "数据面不存在"})
		return
	}
	if len(req.IPs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "没有选中任何 IP"})
		return
	}
	res, err := s.dpPushAll(r.Context(), []string{dp.ID}, req.IPs, "remove", "manual", false)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if len(res) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "该数据面已停用"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": res[0].Status == "ok", "result": res[0], "error": errIfFailed(res[0])})
}

func errIfFailed(r dpPushResult) string {
	if r.Status == "failed" {
		return r.Message
	}
	return ""
}

func (s *Server) handleDPSettings(w http.ResponseWriter, r *http.Request) {
	var req dpreg.Settings
	if !dpDecode(w, r, &req) {
		return
	}
	if err := s.dpStore().SetSettings(req); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": s.dpStore().Settings()})
}
