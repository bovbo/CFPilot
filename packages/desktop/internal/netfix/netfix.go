// Package netfix 解决「本机 DNS 被污染 / 劫持」导致连不上 Cloudflare Pages 控制面的问题。
//
// 现象（真机日志）：
//
//	x509: certificate is valid for *.intel.com, not jooj-b1x.pages.dev   ← 域名被解析到了别人的服务器
//	TLS handshake timeout / EOF                                          ← 解析到了不通的地址
//
// 做法：对 *.pages.dev / *.workers.dev / api.cloudflare.com 这类 Cloudflare 托管域名，不信任系统 DNS，
// 改用 DoH（HTTPS 上的 DNS，直连 1.1.1.1 / 8.8.8.8 的 IP）解析，再按 IP 拨号。
// TLS 仍然校验原域名的证书，所以不降低安全性；DoH 全部失败时退回系统 DNS。
package netfix

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var suffixes = []string{".pages.dev", ".workers.dev"}

// 精确匹配的主机（Cloudflare API）。
var exactHosts = map[string]bool{"api.cloudflare.com": true}

// DoH 服务（用 IP 直连，避免先解析 DoH 域名）。
var dohServers = []string{
	"https://1.1.1.1/dns-query",
	"https://8.8.8.8/resolve",
	"https://1.0.0.1/dns-query",
}

type cacheEntry struct {
	ips []string
	exp time.Time
}

var (
	mu    sync.Mutex
	cache = map[string]cacheEntry{}
	// dohHTTP 可在测试里替换。
	dohHTTP = &http.Client{
		Timeout: 6 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}
)

func matches(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if net.ParseIP(h) != nil {
		return false
	}
	if exactHosts[h] {
		return true
	}
	for _, s := range suffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

type dohAnswer struct {
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

func queryDoH(ctx context.Context, server, name string) ([]string, error) {
	u := server + "?name=" + url.QueryEscape(name) + "&type=A"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	res, err := dohHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("DoH HTTP " + res.Status)
	}
	var out dohAnswer
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out); err != nil {
		return nil, err
	}
	var ips []string
	for _, a := range out.Answer {
		if a.Type == 1 && net.ParseIP(a.Data) != nil {
			ips = append(ips, a.Data)
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("DoH 无 A 记录")
	}
	return ips, nil
}

// Resolve 返回 host 的可信 IP 列表（带 5 分钟缓存）。
func Resolve(ctx context.Context, host string) ([]string, error) {
	key := strings.ToLower(host)
	mu.Lock()
	if e, ok := cache[key]; ok && time.Now().Before(e.exp) {
		mu.Unlock()
		return e.ips, nil
	}
	mu.Unlock()
	var lastErr error
	for _, s := range dohServers {
		ips, err := queryDoH(ctx, s, key)
		if err == nil {
			mu.Lock()
			cache[key] = cacheEntry{ips: ips, exp: time.Now().Add(5 * time.Minute)}
			mu.Unlock()
			return ips, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

var base = &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}

// DialContext 可直接赋给 http.Transport.DialContext。
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !matches(host) {
		return base.DialContext(ctx, network, addr)
	}
	ips, rerr := Resolve(ctx, host)
	if rerr != nil {
		return base.DialContext(ctx, network, addr) // DoH 也不通：退回系统 DNS
	}
	var lastErr error
	for _, ip := range ips {
		c, err := base.DialContext(ctx, "tcp4", net.JoinHostPort(ip, port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	if c, err := base.DialContext(ctx, network, addr); err == nil {
		return c, nil
	}
	return nil, lastErr
}

// Install 把全局默认 HTTP 传输换成带 DoH 兜底的版本（程序启动时调用一次）。
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.DialContext = DialContext
	}
}
