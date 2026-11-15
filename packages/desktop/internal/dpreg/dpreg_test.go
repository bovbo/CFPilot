package dpreg

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// 固定域名 Go 端与内核端必须一致，否则「记录的」和「实际进订阅的」会对不上。
func TestFixedDomainsMatchKernel(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "worker-core", "src", "data", "pools.ts"))
	if err != nil {
		t.Skip("找不到内核源码（单独编译 desktop 时跳过）：", err)
	}
	m := regexp.MustCompile(`(?s)FIXED_PREFERRED_DOMAINS: readonly string\[\] = \[(.*?)\]`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("内核里没有 FIXED_PREFERRED_DOMAINS")
	}
	var kernel []string
	for _, x := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(m[1], -1) {
		kernel = append(kernel, string(x[1]))
	}
	if len(kernel) != len(FixedDomains) {
		t.Fatalf("数量不一致：内核 %d，Go %d", len(kernel), len(FixedDomains))
	}
	for i := range kernel {
		if kernel[i] != FixedDomains[i] {
			t.Fatalf("第 %d 个不一致：内核 %s，Go %s", i, kernel[i], FixedDomains[i])
		}
	}
}

func TestUpsertIsolationAndDedup(t *testing.T) {
	s := New(t.TempDir())
	a, err := s.Upsert(DataPlane{ID: "dp-a", Name: "A", ProfileID: "acc_1", Domain: "a.example.com", UUID: "u1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if a.WSPath != "/" || a.TrojanPath != "/trojan" || a.XHTTPPath != "/xhttp" {
		t.Fatalf("路径默认值不对：%+v", a)
	}
	if _, err := s.Upsert(DataPlane{ID: "dp-b", Name: "B", ProfileID: "acc_2", Domain: "b.example.com", UUID: "u2", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// 同账号同域名不能重复登记
	if _, err := s.Upsert(DataPlane{ID: "dp-c", Name: "C", ProfileID: "acc_1", Domain: "a.example.com", UUID: "u3"}); err == nil {
		t.Fatal("同账号同域名应该被拒绝")
	}
	// 非法域名 / 缺 UUID / 非法 id
	if _, err := s.Upsert(DataPlane{ID: "dp-d", ProfileID: "acc_1", Domain: "bad domain", UUID: "x"}); err == nil {
		t.Fatal("非法域名应该被拒绝")
	}
	if _, err := s.Upsert(DataPlane{ID: "dp-d", ProfileID: "acc_1", Domain: "d.example.com"}); err == nil {
		t.Fatal("缺 UUID 应该被拒绝")
	}
	if _, err := s.Upsert(DataPlane{ID: "Bad_ID", ProfileID: "acc_1", Domain: "d.example.com", UUID: "x"}); err == nil {
		t.Fatal("非法 id 应该被拒绝")
	}
	// 推送记录只写进自己，互不影响；更新保留记录
	_ = s.AddRecord("dp-a", PushRecord{Status: "failed", Message: "x"})
	_ = s.AddRecord("dp-b", PushRecord{Status: "ok"})
	pa, _ := s.Get("dp-a")
	pb, _ := s.Get("dp-b")
	if len(pa.Log) != 1 || pa.Log[0].Status != "failed" || len(pb.Log) != 1 || pb.Log[0].Status != "ok" {
		t.Fatalf("记录串了：%+v / %+v", pa.Log, pb.Log)
	}
	pa.Name = "A2"
	if _, err := s.Upsert(pa); err != nil {
		t.Fatal(err)
	}
	if pa2, _ := s.Get("dp-a"); len(pa2.Log) != 1 || pa2.Name != "A2" {
		t.Fatalf("更新应保留记录：%+v", pa2)
	}
}

func TestPersistAndSettingsDefaults(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	set := s.Settings()
	if !set.PushAfterAdd || !set.PushAfterScan || set.Mode != "append" || set.Top != 15 {
		t.Fatalf("默认设置不对：%+v", set)
	}
	_, _ = s.Upsert(DataPlane{ID: "dp-a", ProfileID: "acc_1", Domain: "a.example.com", UUID: "u1", Enabled: true})
	// 重新打开能读回来，文件权限 0600
	s2 := New(dir)
	if len(s2.List()) != 1 {
		t.Fatal("重启后数据丢失")
	}
	if st, err := os.Stat(filepath.Join(dir, Filename)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("文件权限不对：%v %v", st, err)
	}
	if ok, _ := s2.Delete("dp-a"); !ok || len(s2.List()) != 0 {
		t.Fatal("删除失败")
	}
}

func TestFixedRecordsPlaceholder(t *testing.T) {
	s := New(t.TempDir())
	recs := s.FixedRecords()
	if len(recs) != len(FixedDomains) || recs[0].Note != "尚未测速" {
		t.Fatalf("占位记录不对：%+v", recs)
	}
	_ = s.SetFixed(map[string]FixedRecord{FixedDomains[1]: {Domain: FixedDomains[1], OK: true, LatencyMs: 80}})
	recs = s.FixedRecords()
	if !recs[1].OK || recs[1].LatencyMs != 80 || recs[0].Note != "尚未测速" {
		t.Fatalf("记录不对：%+v", recs)
	}
}
