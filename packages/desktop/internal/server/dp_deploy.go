package server

// 1:N 多数据面：各账号独立部署 / 升级数据面（第 3 阶段）。
//
//	POST /api/dp/deploy   部署或升级一个或多个数据面（异步；进度走日志，结果走 dp-deploy 事件）
//
// ## 设计约束
//
// 1. **每个数据面用它自己所属账号的凭据**（store.Profile），绝不借用「当前活跃账号」，
//    也不会把一个账号的 Token / KV 用到另一个账号上。
// 2. **只部署数据面**（deploy.Options.DataOnly）：不创建、不配置、不部署控制面。
//    账号里已经是完整实例时，也只更新它的数据面，清单里的控制面信息原样保留。
// 3. **逐个串行，失败互不影响**：一个数据面失败只记在它自己名下，其余继续；
//    不做任何自动重试（连续重试会累积账号风控画像，见 deploy.deployOnce 的说明），
//    相邻两次部署之间固定间隔，避免短时间内对多个账号集中发布。
// 4. 与「部署」页共用同一把部署锁：同一时刻只允许一个部署在跑。
// 5. 成功后把域名 / UUID / 路径回读进注册表并同步控制面，订阅聚合立即生效。

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xmwengxing/cfpilot/packages/desktop/internal/deploy"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/dpreg"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/safe"
)

// dpDeployGap 是相邻两个数据面部署之间的固定间隔。
const dpDeployGap = 8 * time.Second

type dpDeployReq struct {
	// IDs 为空 = 全部启用的数据面。
	IDs []string `json:"ids"`
	// Force 对应「强制重新部署」：忽略「线上版本与本地相同则跳过」。
	Force bool `json:"force"`
	// DataPlaneMode：pages / workers；留空 = 沿用账号里已部署的形态（没有则 pages）。
	DataPlaneMode string `json:"dataPlaneMode"`
}

// dpDeployResult 是单个数据面的部署结果。
type dpDeployResult struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	OK            bool     `json:"ok"`
	Skipped       bool     `json:"skipped"`
	Message       string   `json:"message"`
	Warnings      []string `json:"warnings,omitempty"`
	KernelVersion string   `json:"kernelVersion,omitempty"`
}

func (s *Server) handleDPDeploy(w http.ResponseWriter, r *http.Request) {
	var req dpDeployReq
	if !dpDecode(w, r, &req) {
		return
	}
	st := s.dpStore()

	// 选出要部署的数据面
	var targets []dpreg.DataPlane
	if len(req.IDs) == 0 {
		for _, p := range st.List() {
			if p.Enabled {
				targets = append(targets, p)
			}
		}
	} else {
		seen := map[string]bool{}
		for _, id := range req.IDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			p, ok := st.Get(id)
			if !ok {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "数据面不存在：" + id})
				return
			}
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "没有可部署的数据面（都已停用或还没添加）"})
		return
	}

	// 开始前先把每个数据面的前置条件查清楚，缺什么一次说完（缺条件的不会被部署，也不影响其它）。
	var blocked []string
	for _, p := range targets {
		if msg := s.dpDeployPrecheck(p); msg != "" {
			blocked = append(blocked, (p.Name + "：" + msg))
		}
	}
	if len(blocked) == len(targets) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": strings.Join(blocked, "\n")})
		return
	}

	loadCtx, cancelLoad := context.WithTimeout(context.Background(), 90*time.Second)
	artifacts, err := s.loadKernelArtifacts(loadCtx)
	cancelLoad()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.deploy.mu.Lock()
	if s.deploy.running {
		s.deploy.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "已有部署正在进行，请等待完成"})
		return
	}
	s.deploy.running = true
	s.deploy.mu.Unlock()

	plane := strings.ToLower(strings.TrimSpace(req.DataPlaneMode))
	if plane != "pages" && plane != "workers" {
		plane = ""
	}

	go func() {
		defer func() {
			s.deploy.mu.Lock()
			s.deploy.running = false
			s.deploy.mu.Unlock()
		}()
		defer func() {
			if r := safe.Recover("数据面部署任务"); r != nil {
				s.logLevel("数据面部署过程发生内部错误，详情已记入日志（服务继续运行）", "err")
			}
		}()

		results := make([]dpDeployResult, 0, len(targets))
		first := true
		for _, p := range targets {
			if msg := s.dpDeployPrecheck(p); msg != "" {
				s.logLevel("[数据面 "+p.Name+"] 跳过："+msg, "warn")
				results = append(results, dpDeployResult{ID: p.ID, Name: p.Name, Message: "已跳过：" + msg})
				continue
			}
			if !first {
				time.Sleep(dpDeployGap)
			}
			first = false
			res := s.dpDeployOne(p, artifacts.Data, artifacts.Build, artifacts.Version, plane, req.Force)
			results = append(results, res)
		}

		// 一次性同步注册表到控制面（订阅聚合马上用上新的域名 / UUID / 路径）
		sctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := s.dpSyncRegistry(sctx); err != nil {
			s.logLevel("数据面已部署，但同步控制面失败（下次推送时会重试）："+err.Error(), "warn")
		}
		cancel()

		okN := 0
		for _, x := range results {
			if x.OK {
				okN++
			}
		}
		s.logLevel(fmt.Sprintf("数据面部署结束：成功 %d / 共 %d", okN, len(results)), map[bool]string{true: "ok", false: "warn"}[okN == len(results)])
		if s.events != nil {
			s.events.publish("dp-deploy", map[string]any{"at": time.Now().UnixMilli(), "results": results})
		}
	}()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "started": true, "count": len(targets), "skippedPrecheck": blocked,
		"note": "部署已开始，逐个数据面串行执行，进度见运行日志",
	})
}

// dpDeployPrecheck 返回空串表示可部署；否则返回缺什么。
func (s *Server) dpDeployPrecheck(p dpreg.DataPlane) string {
	profile, err := s.store.Get(p.ProfileID)
	if err != nil || profile == nil {
		return "所属账号不存在"
	}
	if strings.TrimSpace(profile.CfToken) == "" {
		return "该账号缺少 API Token"
	}
	if strings.TrimSpace(profile.AccountID) == "" {
		return "该账号缺少 Account ID（请先在凭据页检测账号）"
	}
	if strings.TrimSpace(profile.KVNamespaceID) == "" {
		return "该账号还没有选 KV（请先在凭据页选择或新建 KV）"
	}
	return ""
}

// dpDeployOne 部署（或升级）一个数据面。绝不 panic、不重试；失败只返回在结果里。
func (s *Server) dpDeployOne(p dpreg.DataPlane, dataScript []byte, build, kernelVersion, plane string, force bool) dpDeployResult {
	out := dpDeployResult{ID: p.ID, Name: p.Name}
	tag := "[数据面 " + p.Name + "] "

	profile, err := s.store.Get(p.ProfileID)
	if err != nil || profile == nil {
		out.Message = "所属账号不存在"
		return out
	}

	opt := deploy.DefaultOptions()
	opt.Token = profile.CfToken
	opt.AccountID = profile.AccountID
	opt.KVNamespaceID = profile.KVNamespaceID
	opt.DataScript = dataScript
	opt.DataOnly = true
	opt.Mode = "auto"
	opt.DataProject = strings.TrimSpace(p.WorkerName)
	// 已有身份就带上：重新部署后节点的 UUID / 路径不变，订阅里的老节点继续可用
	opt.UUID = strings.TrimSpace(p.UUID)
	opt.WSPath = strings.TrimSpace(p.WSPath)
	opt.TrojanPassword = strings.TrimSpace(p.TrojanPassword)
	if plane != "" {
		opt.DataPlaneMode = plane
	}
	if opt.DataPlaneMode == "workers" {
		opt.DataHost = strings.TrimSpace(p.Domain)
	}
	opt.PanelPassword = strings.TrimSpace(profile.DeployPanelPassword)
	opt.LocalBuild = build
	opt.LocalKernelVersion = kernelVersion
	opt.KeepCredentials = true
	opt.SkipSameVersion = !force
	opt.Log = func(m string) { s.logf(tag + m) }
	opt.ClaimWrites = s.claimDailyWrite

	s.logf(tag + "开始部署 / 升级（账号：" + profile.Name + "）")
	res, runErr := deploy.New(opt).Run(context.Background())
	if runErr != nil {
		out.Message = runErr.Error()
		s.logLevel(tag+"失败："+runErr.Error(), "err")
		return out
	}
	for _, warn := range res.Warnings {
		out.Warnings = append(out.Warnings, warn)
		s.logLevel(tag+"⚠ "+warn, "warn")
	}
	if res.Skipped {
		out.OK, out.Skipped = true, true
		out.Message = "线上已是最新版本，跳过"
		out.KernelVersion = kernelVersion
		s.logLevel(tag+out.Message, "ok")
		return out
	}
	if strings.TrimSpace(res.Data.Subdomain) == "" {
		out.Message = "数据面部署没有确认成功（详见上面的警告）"
		s.logLevel(tag+out.Message, "err")
		return out
	}

	// 回读：只补空字段，不覆盖你填过的域名（Workers 的自定义域名由你在控制台绑定）
	rctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cur, ok := s.dpStore().Get(p.ID)
	if !ok {
		out.OK = true
		out.Message = "部署完成（数据面已被删除，未回读）"
		return out
	}
	if res.Data.Name != "" && strings.TrimSpace(cur.WorkerName) == "" {
		cur.WorkerName = res.Data.Name
	}
	if err := s.dpReadDeploy(rctx, &cur, false); err != nil {
		out.OK = true
		out.Message = "部署完成，但回读部署清单失败：" + err.Error()
		s.logLevel(tag+out.Message, "warn")
		return out
	}
	if _, err := s.dpStore().Upsert(cur); err != nil {
		out.OK = true
		out.Message = "部署完成，但写入注册表失败：" + err.Error()
		s.logLevel(tag+out.Message, "warn")
		return out
	}
	out.OK = true
	out.KernelVersion = cur.KernelVersion
	out.Message = "部署完成"
	if cur.Domain == "" {
		out.Message += "（还没有域名：Workers 形态请到 Cloudflare 控制台给它绑定自定义域名，再回「编辑」里填上）"
	}
	s.logLevel(tag+out.Message, "ok")
	return out
}
