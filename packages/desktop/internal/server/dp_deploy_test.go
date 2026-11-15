package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xmwengxing/cfpilot/packages/desktop/internal/dpreg"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/store"
)

func postJSONTo(h http.HandlerFunc, body string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, "/api/dp/deploy", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestDPDeployPrecheckReportsWhatIsMissing(t *testing.T) {
	st := newEmptyStore(t)
	s := &Server{store: st}

	if msg := s.dpDeployPrecheck(dpreg.DataPlane{ProfileID: "nope"}); !strings.Contains(msg, "账号不存在") {
		t.Errorf("账号不存在应提示，实际 %q", msg)
	}

	p, err := st.Create("B", &store.Profile{CfToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if msg := s.dpDeployPrecheck(dpreg.DataPlane{ProfileID: p.ID}); !strings.Contains(msg, "Account ID") {
		t.Errorf("缺 Account ID 应提示，实际 %q", msg)
	}
	acc := "acc"
	if _, err := st.Update(p.ID, store.Patch{AccountID: &acc}); err != nil {
		t.Fatal(err)
	}
	if msg := s.dpDeployPrecheck(dpreg.DataPlane{ProfileID: p.ID}); !strings.Contains(msg, "KV") {
		t.Errorf("缺 KV 应提示，实际 %q", msg)
	}
	kv := "kv1"
	if _, err := st.Update(p.ID, store.Patch{KVNamespaceID: &kv}); err != nil {
		t.Fatal(err)
	}
	if msg := s.dpDeployPrecheck(dpreg.DataPlane{ProfileID: p.ID}); msg != "" {
		t.Errorf("条件齐全不该报缺项，实际 %q", msg)
	}
}

// 全部数据面都缺条件 → 直接报错，不起后台任务、不占部署锁。
func TestDPDeployAllBlockedStartsNothing(t *testing.T) {
	st := newEmptyStore(t)
	s := &Server{store: st}
	if _, err := s.dpStore().Upsert(dpreg.DataPlane{ID: "a", Name: "A", ProfileID: "missing", Domain: "a.example.com", UUID: "11111111-1111-4111-8111-111111111111", WSPath: "/ws", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	code, out := postJSONTo(s.handleDPDeploy, `{"ids":["a"]}`)
	if code != http.StatusOK || out["ok"] != false {
		t.Fatalf("应返回 ok=false：%d %v", code, out)
	}
	if e, _ := out["error"].(string); !strings.Contains(e, "A") || !strings.Contains(e, "账号不存在") {
		t.Errorf("错误应点名数据面与原因，实际 %q", e)
	}
	s.deploy.mu.Lock()
	running := s.deploy.running
	s.deploy.mu.Unlock()
	if running {
		t.Error("没有可部署的数据面时不该占用部署锁")
	}
}

func TestDPDeployUnknownIDAndEmptyList(t *testing.T) {
	s := &Server{store: newEmptyStore(t)}
	if _, out := postJSONTo(s.handleDPDeploy, `{"ids":["zzz"]}`); out["ok"] != false {
		t.Errorf("不存在的数据面应报错：%v", out)
	}
	if _, out := postJSONTo(s.handleDPDeploy, `{"ids":[]}`); out["ok"] != false {
		t.Errorf("没有任何数据面应报错：%v", out)
	}
}
