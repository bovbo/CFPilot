package scan

import "testing"

func TestRouterLimitsCap(t *testing.T) {
	defer func() { routerLimits = nil }()
	if got := (Options{}).withDefaults().Concurrency; got != defConcurrency {
		t.Fatalf("未启用限流时应为默认 %d，得到 %d", defConcurrency, got)
	}
	SetRouterLimits(DefaultRouterLimits())
	o := Options{Concurrency: 1000, TLSConcurrency: 500}.withDefaults()
	l := DefaultRouterLimits()
	if o.Concurrency != l.Concurrency || o.TLSConcurrency != l.TLSConcurrency ||
		o.ProbeConcurrency != l.ProbeConcurrency || o.SpeedConnections != l.SpeedConnections ||
		o.ProbeLimit != l.ProbeLimit || o.SpeedLimit != l.SpeedLimit {
		t.Fatalf("限流未生效：%+v", o)
	}
	small := Options{Concurrency: 10}.withDefaults()
	if small.Concurrency != 10 {
		t.Fatalf("低于上限的用户值不应被抬高：%d", small.Concurrency)
	}
}
