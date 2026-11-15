package server

// 固定默认优选域名的「测速并记录」。
//
// ## 规则（用户定的）
//
//   - 这些域名参与每次优选的测速，**只作记录**；
//   - 测速结果**不会**让它们被剔除：它们永远在自定义订阅优选池里
//     （控制面内核保证，见 worker-core 的 FIXED_PREFERRED_DOMAINS）；
//   - 记录失败（解析不了 / 测速失败）只写日志，不影响优选本身。

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/xmwengxing/cfpilot/packages/desktop/internal/dpreg"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/scan"
)

// lookupIPs 可在测试里替换。
var lookupIPs = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

const fixedRecordBudget = 3 * time.Minute

// resolveFixedDomain 解析一个固定域名：优先取最多 2 个 IPv4；没有 IPv4 才取 1 个 IPv6。
func resolveFixedDomain(ctx context.Context, domain string) ([]string, error) {
	rctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	addrs, err := lookupIPs(rctx, domain)
	if err != nil {
		return nil, err
	}
	var v4, v6 []string
	for _, a := range addrs {
		if a.IP.To4() != nil {
			if len(v4) < 2 {
				v4 = append(v4, a.IP.String())
			}
		} else if len(v6) < 1 {
			v6 = append(v6, a.IP.String())
		}
	}
	if len(v4) > 0 {
		return v4, nil
	}
	return v6, nil
}

// recordFixedDomains 对固定域名解析 → 测速 → 记录。绝不返回 error，也不影响调用方。
//
// host / wsPath / uuid / trojan 是本轮优选用的那一份数据面参数，保证口径一致。
func (s *Server) recordFixedDomains(ctx context.Context, host, wsPath, uuid, trojanPassword string) {
	ctx, cancel := context.WithTimeout(ctx, fixedRecordBudget)
	defer cancel()

	byIP := map[string][]string{}
	var cands []string
	recs := map[string]dpreg.FixedRecord{}
	now := time.Now().UnixMilli()
	for _, d := range dpreg.FixedDomains {
		ips, err := resolveFixedDomain(ctx, d)
		if err != nil || len(ips) == 0 {
			note := "解析失败"
			if err != nil {
				note += "：" + err.Error()
			}
			recs[d] = dpreg.FixedRecord{Domain: d, Note: note, At: now}
			continue
		}
		recs[d] = dpreg.FixedRecord{Domain: d, IPs: ips, Note: "测速中", At: now}
		for _, ip := range ips {
			if _, seen := byIP[ip]; !seen {
				cands = append(cands, ip)
			}
			byIP[ip] = append(byIP[ip], d)
		}
	}

	var summary *scan.Summary
	if len(cands) > 0 {
		var err error
		summary, err = scan.Run(ctx, scan.Options{
			Host:            host,
			Port:            443,
			WSPath:          wsPath,
			ProbePath:       scan.ProbeDefaultPath,
			Candidates:      cands,
			Top:             len(cands),
			SpeedDurationMs: 3000,
			VerifyPayload:   true,
			UUID:            strings.TrimSpace(uuid),
			TrojanPassword:  strings.TrimSpace(trojanPassword),
		})
		if err != nil {
			s.logLevel("固定域名测速出错（仅记录，不影响优选）："+err.Error(), "warn")
			summary = nil
		}
	}

	best := map[string]scan.Entry{}
	if summary != nil {
		for _, e := range summary.Entries {
			for _, d := range byIP[e.IP] {
				if cur, ok := best[d]; !ok || e.Speed > cur.Speed {
					best[d] = e
				}
			}
		}
	}
	okCount := 0
	for _, d := range dpreg.FixedDomains {
		r := recs[d]
		if len(r.IPs) == 0 {
			continue
		}
		if e, ok := best[d]; ok {
			r.OK, r.LatencyMs, r.SpeedMBps, r.Colo, r.Note = true, e.Latency, e.Speed, e.Colo, "测速通过（仅记录）"
			okCount++
		} else {
			r.Note = "本轮测速未通过（仅记录；仍保留在订阅里）"
		}
		recs[d] = r
	}
	if err := s.dpStore().SetFixed(recs); err != nil {
		s.logLevel("固定域名记录写入失败："+err.Error(), "warn")
	}
	if summary != nil && len(summary.Entries) > 0 {
		s.saveScanHistory("fixed-domains", host, wsPath, scan.ProbeDefaultPath, false, summary.Entries, "", historyStats(summary), false, 0)
	}
	s.logLevel(fmt.Sprintf("固定优选域名已测速记录：%d/%d 通过（仅记录，9 个域名始终保留在自定义订阅优选池）", okCount, len(dpreg.FixedDomains)), "info")
}
