package scan

// 优选结果回填到面板。
//
// ## 为什么需要
//
// 界面上的「不回填面板，只输出列表」勾选框在 Go 版**完全没有接线** ——
// 勾选状态被读取的地方根本不存在，也就是说无论勾不勾，行为都一样。
//
// 用户实测：「取消勾选后点开始优选，勾选又被自动勾上，无法测试回填」。
// 真实原因是：回填功能压根没实现，界面渲染时又重新渲染了默认值。
//
// ## 契约（与 scanner-cli 的 callback.ts 对齐）
//
//	POST https://<host>/api/v1/preferred-ips
//	Authorization: Bearer <token>
//	Content-Type: application/json
//	{"ips": ["1.2.3.4:443#LAX", ...]}
//
// token 来自面板生成的**一次性扫描令牌**，或内核环境变量 SCANNER_TOKEN。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CallbackResult 是回填结果。
type CallbackResult struct {
	OK     bool
	Status int
	Error  string
}

// PostResults 把优选结果 POST 回面板。
//
// entries 是「可复制」格式的行（`IP:Port#IPv4_SJC圣何塞_12_35.5`），与 CLI 一致。
//
// mode 是写入方式：
//
//	replace  （默认）清空面板优选池后写入
//	append            保留池里已有的 IP，只补真正新的（「增量回填」）
//
// 空串按 replace 处理 —— 历史调用方不传时行为完全不变。
// PushTarget 回填的目标池。
//
//	TargetLocal —— 本地优选池（订阅的 L1 来源）
//	TargetRelay —— 自定义反代入口池（反代优选的产出；只影响出站选路，不进订阅）
type PushTarget string

const (
	TargetLocal PushTarget = "local"
	TargetRelay PushTarget = "relay"
)

func PostResults(ctx context.Context, callbackURL, token string, entries []string, mode string, timeout time.Duration) CallbackResult {
	return PostResultsTo(ctx, callbackURL, token, entries, mode, TargetLocal, timeout)
}

// PostResultsTo 回填到指定的目标池。
func PostResultsTo(ctx context.Context, callbackURL, token string, entries []string, mode string, target PushTarget, timeout time.Duration) CallbackResult {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if mode != "append" {
		mode = "replace"
	}
	if target != TargetRelay {
		target = TargetLocal
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{"ips": entries, "mode": mode, "target": string(target)})
	if err != nil {
		return CallbackResult{Error: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return CallbackResult{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "CFPilot-Desktop/1.0")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return CallbackResult{Error: err.Error()}
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", res.StatusCode)
		} else {
			msg = fmt.Sprintf("HTTP %d %s", res.StatusCode, msg)
		}
		return CallbackResult{Status: res.StatusCode, Error: msg}
	}
	return CallbackResult{OK: true, Status: res.StatusCode}
}

// DefaultCallbackURL 由数据面/控制面域名推导回填地址。
//
// 与 scanner-cli 的默认值一致：`https://<host>/api/v1/preferred-ips`
func DefaultCallbackURL(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "https://" + host
	}
	host = strings.TrimRight(host, "/")
	return host + "/api/v1/preferred-ips"
}

// DPResult 是向某个数据面推送的结果（含内核回报的写入统计）。
type DPResult struct {
	CallbackResult
	// Retryable 表示失败值得重试（网络错误 / 5xx / 429）；4xx 业务错误不重试。
	Retryable bool
	Count     int // 推送后该数据面池内条数
	Added     int // 本次新增（去重后）
	Removed   int // 本次删除
}

// PostResultsDP 把一批 IP 推送到**某一个数据面**的独立优选池（1:N）。
//
// mode：append（增量，去重）/ replace（整体替换）/ remove（按 ip:port 删除）。
// 只写 dp 指定的那个池，不碰全局池，也不影响其它数据面。
func PostResultsDP(ctx context.Context, callbackURL, token, dpID string, entries []string, mode string, timeout time.Duration) DPResult {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if mode != "append" && mode != "remove" {
		mode = "replace"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{"ips": entries, "mode": mode, "target": "local", "dp": dpID})
	if err != nil {
		return DPResult{CallbackResult: CallbackResult{Error: err.Error()}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return DPResult{CallbackResult: CallbackResult{Error: err.Error()}}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "CFPilot-Desktop/1.0")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return DPResult{CallbackResult: CallbackResult{Error: err.Error()}, Retryable: true}
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", res.StatusCode)
		} else {
			msg = fmt.Sprintf("HTTP %d %s", res.StatusCode, msg)
		}
		return DPResult{
			CallbackResult: CallbackResult{Status: res.StatusCode, Error: msg},
			Retryable:      res.StatusCode >= 500 || res.StatusCode == 429,
		}
	}
	var out struct {
		Count   int `json:"count"`
		Added   int `json:"added"`
		Removed int `json:"removed"`
	}
	_ = json.Unmarshal(raw, &out)
	return DPResult{
		CallbackResult: CallbackResult{OK: true, Status: res.StatusCode},
		Count:          out.Count, Added: out.Added, Removed: out.Removed,
	}
}

// RegistryItem 是同步到控制面的数据面描述（只含渲染订阅所需字段）。
type RegistryItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Domain         string `json:"domain"`
	UUID           string `json:"uuid"`
	WSPath         string `json:"wsPath"`
	TrojanPassword string `json:"trojanPassword"`
	TrojanPath     string `json:"trojanPath"`
	XHTTPPath      string `json:"xhttpPath"`
	Enabled        bool   `json:"enabled"`
}

// DataplanesURL 由控制面域名推导注册表接口地址。
func DataplanesURL(host string) string {
	return strings.TrimSuffix(DefaultCallbackURL(host), "/preferred-ips") + "/dataplanes"
}

// PutRegistry 把数据面注册表同步到控制面（扫描令牌鉴权）。
func PutRegistry(ctx context.Context, host, token string, items []RegistryItem, timeout time.Duration) CallbackResult {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return CallbackResult{Error: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, DataplanesURL(host), bytes.NewReader(body))
	if err != nil {
		return CallbackResult{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "CFPilot-Desktop/1.0")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return CallbackResult{Error: err.Error()}
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return CallbackResult{Status: res.StatusCode, Error: fmt.Sprintf("HTTP %d %s", res.StatusCode, strings.TrimSpace(string(raw)))}
	}
	return CallbackResult{OK: true, Status: res.StatusCode}
}

// PoolCounts 读取控制面上各数据面独立池的条数（扫描令牌鉴权）。
func PoolCounts(ctx context.Context, host, token string, timeout time.Duration) (map[string]int, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DataplanesURL(host), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "CFPilot-Desktop/1.0")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var out struct {
		Items []struct {
			ID        string `json:"id"`
			PoolCount int    `json:"poolCount"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	m := map[string]int{}
	for _, it := range out.Items {
		m[it.ID] = it.PoolCount
	}
	return m, nil
}
