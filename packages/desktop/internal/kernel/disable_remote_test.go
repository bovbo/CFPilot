package kernel

import (
	"context"
	"testing"
)

// 路由器版禁用远程内核后：不联网，直接用内置内核。
func TestDisableRemoteUsesBuiltin(t *testing.T) {
	DisableRemote = true
	defer func() { DisableRemote = false }()
	if info, err := FetchRemoteInfo(context.Background()); info != nil || err != nil {
		t.Fatalf("应直接返回空：%v %v", info, err)
	}
	if info, has := CheckChannelUpdate(context.Background(), "0.50.0"); info != nil || has {
		t.Fatal("通道检查应被禁用")
	}
	a, info, err := LoadPreferred(context.Background(), "0.50.0")
	if err != nil || a == nil || info != nil {
		t.Fatalf("应使用内置内核：a=%v info=%v err=%v", a, info, err)
	}
	if a.Source == "" || len(a.Control) == 0 || len(a.Data) == 0 {
		t.Fatalf("内置内核内容为空：%+v", a.Source)
	}
}
