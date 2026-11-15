package server

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestResolveFixedDomainPrefersV4(t *testing.T) {
	old := lookupIPs
	defer func() { lookupIPs = old }()
	lookupIPs = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("2606:4700::1")},
			{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP("1.0.0.1")}, {IP: net.ParseIP("8.8.8.8")},
		}, nil
	}
	got, err := resolveFixedDomain(context.Background(), "x.example.com")
	if err != nil || len(got) != 2 || got[0] != "1.1.1.1" || got[1] != "1.0.0.1" {
		t.Fatalf("应只取前 2 个 IPv4：%v %v", got, err)
	}
}

func TestResolveFixedDomainFallsBackToV6AndErrors(t *testing.T) {
	old := lookupIPs
	defer func() { lookupIPs = old }()
	lookupIPs = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("2606:4700::1")}, {IP: net.ParseIP("2606:4700::2")}}, nil
	}
	got, _ := resolveFixedDomain(context.Background(), "x6.example.com")
	if len(got) != 1 || got[0] != "2606:4700::1" {
		t.Fatalf("没有 v4 时取 1 个 v6：%v", got)
	}
	lookupIPs = func(ctx context.Context, host string) ([]net.IPAddr, error) { return nil, errors.New("nxdomain") }
	if _, err := resolveFixedDomain(context.Background(), "nope.example.com"); err == nil {
		t.Fatal("解析失败应返回错误")
	}
}

func TestParseIPLine(t *testing.T) {
	cases := []struct {
		in   string
		ip   string
		port int
		ok   bool
	}{
		{"1.2.3.4:443#SJC", "1.2.3.4", 443, true},
		{"1.2.3.4", "1.2.3.4", 443, true},
		{"[2606:4700::1]:8443#x", "2606:4700::1", 8443, true},
		{"2606:4700::1", "2606:4700::1", 443, true},
		{"", "", 0, false},
		{"1.2.3.4:abc", "", 0, false},
		{"[2606::1", "", 0, false},
	}
	for _, c := range cases {
		got, ok := parseIPLine(c.in)
		if ok != c.ok || (ok && (got.IP != c.ip || got.Port != c.port)) {
			t.Fatalf("%q → %+v %v，期望 %s:%d %v", c.in, got, ok, c.ip, c.port, c.ok)
		}
	}
}
