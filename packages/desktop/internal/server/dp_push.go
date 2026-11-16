package server

// 1:N 多数据面：统一优选结果的「重新验证 → 分发 → 推送」。
//
// ## 约束（逐条对应需求）
//
//   - 每个数据面各自独立：重新验证用**它自己的域名 / 路径 / UUID**，推送只写**它自己的池**，
//     失败只记在它自己的记录里，不影响其它数据面；
//   - 推送前必须重新验证 TLS + WS：不是它域名下能用的 IP 一个也不会写进它的池；
//   - 验证后一个可用 IP 都没有时**不写**（尤其 replace 模式，绝不能把原池清空）；
//   - 失败自动重试（网络错误 / 5xx / 429），业务错误（4xx）不重试；
//   - 去重由内核完成（append 按 ip:port 去重）；
//   - 同一时刻只允许一轮推送，避免定时任务与手动操作互相叠加（路由器上尤其重要）。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmwengxing/cfpilot/packages/desktop/internal/dpreg"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/scan"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/schedule"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/store"
)

const (
	dpPushAttempts   = 3
	dpValidateBudget = 3 * time.Minute
	dpPushBudget     = 12 * time.Minute
)

var (
	dpPushLock sync.Mutex
	dpStores   sync.Map // dir → *dpreg.Store（同一目录共用一个实例，写入才不会互相覆盖）
)

// dpPushing 表示当前是否有推送在跑（界面轮询用）。
var dpPushing struct {
	sync.Mutex
	on bool
}

func (s *Server) dpStore() *dpreg.Store {
	dir := s.store.DirPath()
	if v, ok := dpStores.Load(dir); ok {
		return v.(*dpreg.Store)
	}
	v, _ := dpStores.LoadOrStore(dir, dpreg.New(dir))
	return v.(*dpreg.Store)
}

// dpPushResult 是单个数据面一次推送的结果。
type dpPushResult struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Status   string `json:"status"` // ok / failed / skipped
	Message  string `json:"message"`
	Checked  int    `json:"checked"`
	Valid    int    `json:"valid"`
	Added    int    `json:"added"`
	Pool     int    `json:"pool"`
	Attempts int    `json:"attempts"`
}

// ipLine 是解析后的一行 `IP:Port#备注`。
type ipLine struct {
	Raw  string
	IP   string
	Port int
}

// parseIPLine 解析 `1.2.3.4:443#备注` / `[2606::1]:443#备注` / `1.2.3.4`。
func parseIPLine(raw string) (ipLine, bool) {
	l := strings.TrimSpace(raw)
	if l == "" {
		return ipLine{}, false
	}
	core := l
	if i := strings.Index(core, "#"); i >= 0 {
		core = strings.TrimSpace(core[:i])
	}
	port := 443
	ip := core
	if strings.HasPrefix(core, "[") {
		end := strings.Index(core, "]")
		if end < 0 {
			return ipLine{}, false
		}
		ip = core[1:end]
		if rest := core[end+1:]; strings.HasPrefix(rest, ":") {
			p, err := strconv.Atoi(rest[1:])
			if err != nil {
				return ipLine{}, false
			}
			port = p
		}
	} else if strings.Count(core, ":") == 1 {
		i := strings.Index(core, ":")
		p, err := strconv.Atoi(core[i+1:])
		if err != nil {
			return ipLine{}, false
		}
		ip, port = core[:i], p
	}
	if ip == "" || port <= 0 || port > 65535 {
		return ipLine{}, false
	}
	return ipLine{Raw: l, IP: ip, Port: port}, true
}

// dpControl 取控制面的域名与面板密码。
func (s *Server) dpControl(ctx context.Context) (string, string, error) {
	set := s.dpStore().Settings()
	var profile *store.Profile
	if set.ControlProfileID != "" {
		p, err := s.store.Get(set.ControlProfileID)
		if err != nil || p == nil {
			return "", "", fmt.Errorf("设置里指定的控制面账号已不存在")
		}
		profile = p
	} else {
		p, err := s.store.Active()
		if err != nil || p == nil {
			return "", "", fmt.Errorf("没有可用的控制面账号，请先添加账号并完成控制面部署")
		}
		profile = p
	}
	if strings.TrimSpace(profile.CfToken) == "" {
		return "", "", fmt.Errorf("控制面账号没有 API Token")
	}
	manifest, host, password := s.resolvePanelAccess(ctx, profile)
	if host == "" {
		return "", "", fmt.Errorf("控制面账号的清单里没有控制面域名，请先完成一次部署")
	}
	if password == "" && manifest != nil {
		password = strings.TrimSpace(manifest.Credentials.PanelPassword)
	}
	if password == "" {
		password = strings.TrimSpace(profile.PanelPassword)
	}
	if password == "" {
		return "", "", fmt.Errorf("找不到控制面的面板密码，请先在部署页填写并保存")
	}
	return host, password, nil
}

// dpRegistryItems 把本地注册表转成控制面需要的描述（不含任何账号凭据）。
func dpRegistryItems(planes []dpreg.DataPlane) []scan.RegistryItem {
	items := make([]scan.RegistryItem, 0, len(planes))
	for _, p := range planes {
		items = append(items, scan.RegistryItem{
			ID: p.ID, Name: p.Name, Domain: p.Domain, UUID: p.UUID,
			WSPath: p.WSPath, TrojanPassword: p.TrojanPassword,
			TrojanPath: p.TrojanPath, XHTTPPath: p.XHTTPPath, Enabled: p.Enabled,
		})
	}
	return items
}

// dpSyncRegistry 把注册表同步到控制面（订阅聚合依赖它）。
func (s *Server) dpSyncRegistry(ctx context.Context) error {
	host, password, err := s.dpControl(ctx)
	if err != nil {
		return err
	}
	tok := scan.FetchScannerToken(ctx, host, password, 25*time.Second)
	if !tok.OK {
		return fmt.Errorf("获取控制面令牌失败：%s", tok.Error)
	}
	res := scan.PutRegistry(ctx, host, tok.Token, dpRegistryItems(s.dpStore().List()), 20*time.Second)
	if !res.OK {
		return fmt.Errorf("同步数据面注册表失败：%s", dpOldControlHint(res.Error))
	}
	return nil
}

// dpRevalidate 用该数据面**自己的**域名 / 路径 / UUID 重新验证一批 IP（TLS + WS + 真实隧道）。
//
// 返回通过验证的原始行（保留备注与顺序）。端口不是 443 的行不参与验证，也不会被推送。
func (s *Server) dpRevalidate(ctx context.Context, dp dpreg.DataPlane, lines []string) ([]string, int, error) {
	var ips []string
	byIP := map[string][]string{}
	for _, raw := range lines {
		pl, ok := parseIPLine(raw)
		if !ok || pl.Port != 443 {
			continue
		}
		if _, seen := byIP[pl.IP]; !seen {
			ips = append(ips, pl.IP)
		}
		byIP[pl.IP] = append(byIP[pl.IP], pl.Raw)
	}
	if len(ips) == 0 {
		return nil, 0, nil
	}
	vctx, cancel := context.WithTimeout(ctx, dpValidateBudget)
	defer cancel()
	summary, err := scan.Run(vctx, scan.Options{
		Host:           dp.Domain,
		Port:           443,
		WSPath:         dp.WSPath,
		ProbePath:      scan.ProbeDefaultPath,
		ProbeTransport: "ws",
		Candidates:     ips,
		SkipSpeed:      true, // 这里只确认「在它的域名下能连通」，不重复测速
		Top:            len(ips),
		VerifyPayload:  true,
		UUID:           dp.UUID,
		TrojanPassword: dp.TrojanPassword,
	})
	if err != nil {
		return nil, len(ips), err
	}
	pass := map[string]bool{}
	for _, e := range summary.Entries {
		pass[e.IP] = true
	}
	var valid []string
	for _, ip := range ips {
		if pass[ip] {
			valid = append(valid, byIP[ip]...)
		}
	}
	return valid, len(ips), nil
}

// dpPushOne 向一个数据面推送（验证 + 重试 + 记录）。绝不返回 error —— 失败写进结果。
func (s *Server) dpPushOne(ctx context.Context, host, token string, dp dpreg.DataPlane, lines []string, mode, reason string) dpPushResult {
	r := dpPushResult{ID: dp.ID, Name: dp.Name, Domain: dp.Domain}
	record := func() dpPushResult {
		_ = s.dpStore().AddRecord(dp.ID, dpreg.PushRecord{
			At: time.Now().UnixMilli(), Reason: reason, Mode: mode, Status: r.Status, Message: r.Message,
			Checked: r.Checked, Valid: r.Valid, Added: r.Added, Pool: r.Pool, Attempts: r.Attempts,
		})
		return r
	}

	push := lines
	if mode != "remove" {
		valid, checked, err := s.dpRevalidate(ctx, dp, lines)
		r.Checked = checked
		if err != nil {
			r.Status, r.Message = "failed", "重新验证出错："+err.Error()
			return record()
		}
		r.Valid = len(valid)
		if len(valid) == 0 {
			r.Status = "skipped"
			r.Message = fmt.Sprintf("在 %s 下验证了 %d 个 IP，没有一个通过 TLS+WS；未写入，原有池保持不变", dp.Domain, checked)
			return record()
		}
		push = valid
	}

	delays := []time.Duration{2 * time.Second, 6 * time.Second}
	var last scan.DPResult
	for attempt := 1; attempt <= dpPushAttempts; attempt++ {
		r.Attempts = attempt
		last = scan.PostResultsDP(ctx, scan.DefaultCallbackURL(host), token, dp.ID, push, mode, 25*time.Second)
		if last.OK || !last.Retryable || attempt == dpPushAttempts {
			break
		}
		select {
		case <-ctx.Done():
			r.Status, r.Message = "failed", "已取消："+ctx.Err().Error()
			return record()
		case <-time.After(delays[attempt-1]):
		}
	}
	if !last.OK {
		r.Status, r.Message = "failed", last.Error
		return record()
	}
	r.Status = "ok"
	r.Added, r.Pool = last.Added, last.Count
	if mode == "remove" {
		r.Message = fmt.Sprintf("已删除 %d 条", last.Removed)
	} else if r.Valid < r.Checked {
		r.Message = fmt.Sprintf("验证通过 %d/%d，新增 %d 条", r.Valid, r.Checked, last.Added)
	} else {
		r.Message = fmt.Sprintf("新增 %d 条", last.Added)
	}
	return record()
}

// dpPushAll 把 lines 分发到所选数据面（ids 为空 = 全部符合条件的）。
//
// auto=true（扫描后自动推送）只推 Enabled && AutoPush 的数据面；
// auto=false（手动 / 新增后）只看 Enabled。每个数据面串行处理、互不影响。
func (s *Server) dpPushAll(ctx context.Context, ids []string, lines []string, mode, reason string, auto bool) ([]dpPushResult, error) {
	if !dpPushLock.TryLock() {
		return nil, fmt.Errorf("已有一轮推送正在执行，请稍后再试")
	}
	defer dpPushLock.Unlock()
	dpPushing.Lock()
	dpPushing.on = true
	dpPushing.Unlock()
	defer func() {
		dpPushing.Lock()
		dpPushing.on = false
		dpPushing.Unlock()
		if s.events != nil {
			s.events.publish("dp-push", map[string]any{"at": schedule.NowMS()})
		}
	}()

	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var targets []dpreg.DataPlane
	for _, dp := range s.dpStore().List() {
		if !dp.Enabled || (len(want) > 0 && !want[dp.ID]) {
			continue
		}
		if auto && !dp.AutoPush {
			continue
		}
		targets = append(targets, dp)
	}
	if len(targets) == 0 {
		return nil, nil
	}
	if len(lines) == 0 && mode != "remove" {
		return nil, fmt.Errorf("没有可推送的优选结果")
	}

	ctx, cancel := context.WithTimeout(ctx, dpPushBudget)
	defer cancel()

	fail := func(msg string) ([]dpPushResult, error) {
		out := make([]dpPushResult, 0, len(targets))
		for _, dp := range targets {
			r := dpPushResult{ID: dp.ID, Name: dp.Name, Domain: dp.Domain, Status: "failed", Message: msg}
			_ = s.dpStore().AddRecord(dp.ID, dpreg.PushRecord{At: time.Now().UnixMilli(), Reason: reason, Mode: mode, Status: "failed", Message: msg})
			out = append(out, r)
		}
		return out, nil
	}

	host, password, err := s.dpControl(ctx)
	if err != nil {
		return fail(err.Error())
	}
	var tok scan.TokenResult
	for i := 0; i < 2; i++ {
		tok = scan.FetchScannerToken(ctx, host, password, 25*time.Second)
		if tok.OK {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !tok.OK {
		return fail("获取控制面令牌失败：" + tok.Error)
	}
	// 先同步注册表：内核只接受已注册的数据面
	if reg := scan.PutRegistry(ctx, host, tok.Token, dpRegistryItems(s.dpStore().List()), 20*time.Second); !reg.OK {
		return fail("同步数据面注册表失败：" + dpOldControlHint(reg.Error))
	}

	results := make([]dpPushResult, 0, len(targets))
	for _, dp := range targets {
		if ctx.Err() != nil {
			r := dpPushResult{ID: dp.ID, Name: dp.Name, Domain: dp.Domain, Status: "failed", Message: "本轮超时，未处理"}
			_ = s.dpStore().AddRecord(dp.ID, dpreg.PushRecord{At: time.Now().UnixMilli(), Reason: reason, Mode: mode, Status: "failed", Message: r.Message})
			results = append(results, r)
			continue
		}
		results = append(results, s.dpPushOne(ctx, host, tok.Token, dp, lines, mode, reason))
	}
	return results, nil
}

// dpLogResults 把一轮推送结果写进运行日志（每个数据面一行）。
func (s *Server) dpLogResults(title string, results []dpPushResult) {
	for _, r := range results {
		level := "ok"
		switch r.Status {
		case "failed":
			level = "warn"
		case "skipped":
			level = "info"
		}
		s.logLevel(fmt.Sprintf("%s → %s（%s）：%s", title, r.Name, r.Domain, r.Message), level)
	}
}

// dpAutoPush 在一次优选成功后自动推送到各数据面。失败只记日志，不影响优选本身。
func (s *Server) dpAutoPush(lines []string, reason string) {
	st := s.dpStore()
	set := st.Settings()
	if !set.PushAfterScan || len(st.List()) == 0 || len(lines) == 0 {
		return
	}
	if len(lines) > set.Top {
		lines = lines[:set.Top]
	}
	results, err := s.dpPushAll(context.Background(), nil, lines, set.Mode, reason, true)
	if err != nil {
		s.logLevel("数据面自动推送未执行："+err.Error(), "warn")
		return
	}
	s.dpLogResults("数据面推送", results)
}

// dpSourceLines 取「当前最新的优选结果」：定时优选 IP 池顶部的前 n 条。
func (s *Server) dpSourceLines(n int) []string {
	pool := s.scheduleStore().Pool()
	schedule.SortPool(pool)
	if n > 0 && len(pool) > n {
		pool = pool[:n]
	}
	return schedule.CopyLines(pool)
}

// dpOldControlHint 给「控制面是旧版内核」这类最常见的 401 / 404 加上一句人话说明。
//
// 多数据面接口（/api/scanner/dataplanes 等）是新版内核才有的；线上还是旧版控制面时，
// 请求会落到普通会话鉴权上，返回「未登录或会话已过期」的 401，容易被误以为是密码错了。
func dpOldControlHint(msg string) string {
	if strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 404") || strings.Contains(msg, "HTTP 405") {
		return msg + "\n   → 很可能是控制面还是旧版内核（不支持多数据面接口）。请到「部署」页重新部署一次控制面，把内核升级到当前程序自带的新版本，再重试。"
	}
	return msg
}
