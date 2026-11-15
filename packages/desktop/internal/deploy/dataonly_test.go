package deploy

// 「仅数据面」部署（1:N 多数据面：每个账号只放一个数据面 Worker）的规则测试。
//
// 重点是「不碰控制面」和「不越界覆盖」两条：
//   - 不创建、不配置（PATCH）、不部署控制面；
//   - 清单属于另一个实例时，fresh 仍被归属校验拒绝；
//   - 更新完整实例时，清单里的控制面信息原样保留。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func dataOnlyOpts(o *Options) {
	o.DataOnly = true
	o.ControlScript = nil
	o.Mode = "auto"
	o.LocalBuild = "new-build"
	o.WaitTimeout = 2 * time.Second
	o.WaitInterval = 10 * time.Millisecond
}

func TestDataOnlyFirstDeploy(t *testing.T) {
	f := newFakeCF()
	d, _ := newTestDeployer(t, f, dataOnlyOpts)

	res, err := d.Run(context.Background())
	if err != nil {
		t.Fatalf("仅数据面首次部署失败：%v", err)
	}
	if res.Data.Name == "" {
		t.Error("应生成数据面项目名")
	}
	if res.Control.Name != "" {
		t.Errorf("仅数据面不该生成控制面项目名，实际 %q", res.Control.Name)
	}
	if len(f.created) != 1 || f.created[0] != res.Data.Name {
		t.Errorf("只应创建数据面一个项目，实际 %v", f.created)
	}
	if len(f.deployOrder) != 1 || f.deployOrder[0] != res.Data.Name {
		t.Errorf("只应部署数据面，实际 %v", f.deployOrder)
	}
	raw := f.kvWrites["CFP_DEPLOY_MANIFEST"]
	if raw == "" {
		t.Fatal("仅数据面部署也必须写入部署清单（后续更新与「读取部署」靠它）")
	}
	if !strings.Contains(raw, `"controlProject": ""`) {
		t.Errorf("清单里控制面应为空串，实际：%s", raw)
	}
	if !strings.Contains(raw, res.Data.Name) {
		t.Errorf("清单里应有数据面项目名：%s", raw)
	}
	if f.kvWrites["CONFIG"] == "" {
		t.Error("应写入大写 CONFIG")
	}
}

func TestDataOnlyUpdateKeepsOwnCredentialsAndName(t *testing.T) {
	f := newFakeCF()
	f.kvSeed["CFP_DEPLOY_MANIFEST"] = `{
		"v": 1, "controlProject": "", "dataProject": "dp-existing",
		"build": "old-build", "at": "2026-11-01T00:00:00.000Z",
		"credentials": {"uuid":"KEEP-UUID","wsPath":"/keep-ws","panelPath":"/p","panelPassword":"pw","trojanPassword":"tp"}
	}`
	d, _ := newTestDeployer(t, f, dataOnlyOpts)

	res, err := d.Run(context.Background())
	if err != nil {
		t.Fatalf("纯数据面清单应判为本实例、原地更新：%v", err)
	}
	if res.Data.Name != "dp-existing" {
		t.Errorf("应复用清单里的数据面项目名，实际 %q", res.Data.Name)
	}
	if res.UUID != "KEEP-UUID" {
		t.Errorf("应沿用 UUID（订阅里的节点不变），实际 %q", res.UUID)
	}
	if len(f.created) > 1 {
		t.Errorf("不该创建控制面，实际创建 %v", f.created)
	}
	for _, n := range f.deployOrder {
		if n != "dp-existing" {
			t.Errorf("只应部署数据面，却部署了 %q", n)
		}
	}
}

func TestDataOnlyUpdateOfFullInstanceDoesNotTouchControl(t *testing.T) {
	f := newFakeCF()
	f.kvSeed["CFP_DEPLOY_MANIFEST"] = `{
		"v": 1, "controlProject": "ctl-live", "dataProject": "dp-live",
		"controlSubdomain": "ctl-live.pages.dev", "dataSubdomain": "dp-live.pages.dev",
		"build": "old-build", "at": "2026-11-01T00:00:00.000Z",
		"credentials": {"uuid":"U","wsPath":"/w","panelPath":"/p","panelPassword":"pw","trojanPassword":"tp"}
	}`
	d, _ := newTestDeployer(t, f, dataOnlyOpts)

	if _, err := d.Run(context.Background()); err != nil {
		t.Fatalf("更新完整实例的数据面不该被拒：%v", err)
	}
	for _, n := range f.deployOrder {
		if n == "ctl-live" {
			t.Error("仅数据面更新绝不能部署控制面")
		}
	}
	if f.patchCalls["ctl-live"] != 0 {
		t.Errorf("绝不能 PATCH 控制面（会触发隐式部署），实际 %d 次", f.patchCalls["ctl-live"])
	}
	raw := f.kvWrites["CFP_DEPLOY_MANIFEST"]
	if !strings.Contains(raw, `"controlProject": "ctl-live"`) {
		t.Errorf("清单里的控制面信息必须原样保留：%s", raw)
	}
}

func TestDataOnlyFreshRejectedWhenManifestExists(t *testing.T) {
	f := newFakeCF()
	f.kvSeed["CFP_DEPLOY_MANIFEST"] = `{
		"v": 1, "controlProject": "ctl-live", "dataProject": "dp-live",
		"build": "b", "at": "2026-11-01T00:00:00.000Z", "credentials": {"uuid":"U"}
	}`
	d, _ := newTestDeployer(t, f, func(o *Options) {
		dataOnlyOpts(o)
		o.Mode = "fresh"
	})
	if _, err := d.Run(context.Background()); err == nil {
		t.Fatal("fresh 遇到已有实例必须被归属校验拒绝，不能越界覆盖")
	}
	if len(f.deployOrder) != 0 || len(f.kvWrites) != 0 {
		t.Errorf("被拒后不该有任何部署或写入：部署 %v，写入 %v", f.deployOrder, f.kvWrites)
	}
}

func TestDataOnlyStillRequiresDataScript(t *testing.T) {
	f := newFakeCF()
	d, _ := newTestDeployer(t, f, func(o *Options) {
		dataOnlyOpts(o)
		o.DataScript = nil
	})
	if _, err := d.Run(context.Background()); err == nil {
		t.Fatal("数据面产物为空必须报错")
	}
}
