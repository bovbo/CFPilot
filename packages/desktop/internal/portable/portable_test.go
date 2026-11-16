package portable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetupAtMigratesAndRedirects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, k := range []string{EnvDataDir, EnvPoolDir, EnvKernelCache, EnvUpdateDir} {
		t.Setenv(k, "")
	}
	old := filepath.Join(home, ".cfpilot-gui")
	_ = os.MkdirAll(old, 0o755)
	_ = os.WriteFile(filepath.Join(old, "secrets.json"), []byte(`{"x":1}`), 0o600)
	_ = os.WriteFile(filepath.Join(old, "cfpilot.log"), []byte("log"), 0o600)
	_ = os.WriteFile(filepath.Join(old, "cfpilot-0.5.0.exe"), []byte("exe"), 0o600)

	root := filepath.Join(t.TempDir(), "代理微内核面板")
	_ = os.MkdirAll(root, 0o755)
	res := setupAt(root)
	if !res.Enabled || res.DataDir != filepath.Join(root, "data") {
		t.Fatalf("应启用便携模式：%+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(root, "data", "secrets.json")); err != nil || string(b) != `{"x":1}` {
		t.Fatalf("凭据应被复制过来：%v %s", err, b)
	}
	if _, err := os.Stat(filepath.Join(root, "data", "cfpilot.log")); err == nil {
		t.Fatal("日志不应被复制")
	}
	if _, err := os.Stat(filepath.Join(root, "data", "cfpilot-0.5.0.exe")); err == nil {
		t.Fatal("exe 不应被复制")
	}
	if _, err := os.Stat(filepath.Join(old, "secrets.json")); err != nil {
		t.Fatal("旧位置的文件必须保留")
	}
	if os.Getenv(EnvDataDir) != filepath.Join(root, "data") || DataDir() != filepath.Join(root, "data") {
		t.Fatal("数据目录环境变量未设置")
	}
	if UpdateDir() != filepath.Join(root, "updates") {
		t.Fatalf("升级目录不对：%s", UpdateDir())
	}

	// 第二次：已有 secrets.json，不再复制、不覆盖
	_ = os.WriteFile(filepath.Join(root, "data", "secrets.json"), []byte(`{"new":1}`), 0o600)
	res2 := setupAt(root)
	if len(res2.Migrated) != 0 {
		t.Fatalf("不应再次迁移：%v", res2.Migrated)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "data", "secrets.json")); string(b) != `{"new":1}` {
		t.Fatal("已有数据不能被覆盖")
	}
}

func TestSetupHonorsUserEnv(t *testing.T) {
	t.Setenv(EnvDataDir, "/somewhere")
	if r := Setup(); r.Enabled {
		t.Fatal("用户设置了环境变量时不应启用便携模式")
	}
}
