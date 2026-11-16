// Package portable 让程序变成「绿色便携版」：数据 / 升级包 / 内核缓存全放在 exe 同目录下，
// 整个文件夹拷到哪里（U 盘、另一块盘）都能直接用，不往 C:\Users 里写东西。
//
// 目录布局（以 E:\代理微内核面板 为例）：
//
//	E:\代理微内核面板\
//	├─ CFPilot.exe         程序本体
//	├─ data\               凭据、设置、日志、优选历史、定时任务、优选池 …（备份这个文件夹即可）
//	│   ├─ pool\           优选池
//	│   └─ kernel\         内核缓存
//	└─ updates\            一键更新下载的新版本
//
// 只靠环境变量把各处的默认位置重定向过来，其余代码不用改：
// 用户自己设置了环境变量（CFPILOT_DATA_DIR 等）时一律以用户的为准。
package portable

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 与各模块约定的环境变量名（保持与它们一致）。
const (
	EnvDataDir     = "CFPILOT_DATA_DIR"
	EnvPoolDir     = "CFPILOT_CONFIG_DIR"
	EnvKernelCache = "CFPILOT_KERNEL_CACHE_DIR"
	EnvUpdateDir   = "CFPILOT_UPDATE_DIR"
)

// Result 是 Setup 的结果，用来写启动日志。
type Result struct {
	Enabled  bool     // 是否启用了便携模式
	Root     string   // exe 所在目录
	DataDir  string   // 数据目录
	Migrated []string // 从旧位置复制过来的文件
	Reason   string   // 没启用时的原因
}

// DataDir 返回数据目录：环境变量优先，否则 ~/.cfpilot-gui。
// 给不方便依赖 config 包的模块用（避免循环引用）。
func DataDir() string {
	if v := strings.TrimSpace(os.Getenv(EnvDataDir)); v != "" {
		return v
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".cfpilot-gui")
	}
	return ".cfpilot-gui"
}

// UpdateDir 返回新版本下载目录：环境变量优先，否则 数据目录/updates。
func UpdateDir() string {
	if v := strings.TrimSpace(os.Getenv(EnvUpdateDir)); v != "" {
		return v
	}
	return filepath.Join(DataDir(), "updates")
}

// Setup 启用便携模式。必须在读取任何配置之前调用（越早越好）。
//
// 不会启用的情况（都会安静地回退到原来的位置，不影响使用）：
//   - 用户已经设置了 CFPILOT_DATA_DIR；
//   - exe 所在目录不可写（比如装在 Program Files）。
func Setup() Result {
	if strings.TrimSpace(os.Getenv(EnvDataDir)) != "" {
		return Result{Reason: "已设置环境变量 " + EnvDataDir + "，按你的设置走"}
	}
	exe, err := os.Executable()
	if err != nil {
		return Result{Reason: "无法定位程序位置：" + err.Error()}
	}
	if real, e := filepath.EvalSymlinks(exe); e == nil {
		exe = real
	}
	return setupAt(filepath.Dir(exe))
}

func setupAt(root string) Result {
	res := Result{Root: root}
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		res.Reason = "程序所在目录不可写，改用用户目录：" + err.Error()
		return res
	}
	// 真写一个文件试试，目录存在不代表能写
	probe := filepath.Join(data, ".write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		res.Reason = "程序所在目录不可写，改用用户目录：" + err.Error()
		return res
	}
	_ = os.Remove(probe)

	pool := filepath.Join(data, "pool")
	kcache := filepath.Join(data, "kernel")
	updates := filepath.Join(root, "updates")
	for _, d := range []string{pool, kcache} {
		_ = os.MkdirAll(d, 0o755)
	}

	// 第一次启用：把旧位置（~/.cfpilot-gui、~/.cfpilot）里的数据复制过来（只复制，不删除旧的）
	if _, err := os.Stat(filepath.Join(data, "secrets.json")); os.IsNotExist(err) {
		if home, herr := os.UserHomeDir(); herr == nil {
			res.Migrated = append(res.Migrated, copyTopLevel(filepath.Join(home, ".cfpilot-gui"), data)...)
			res.Migrated = append(res.Migrated, copyTopLevel(filepath.Join(home, ".cfpilot"), pool)...)
		}
	}

	_ = os.Setenv(EnvDataDir, data)
	_ = os.Setenv(EnvPoolDir, pool)
	_ = os.Setenv(EnvKernelCache, kcache)
	_ = os.Setenv(EnvUpdateDir, updates)
	res.Enabled = true
	res.DataDir = data
	return res
}

// copyTopLevel 复制 src 目录下的普通文件（不递归、不覆盖已有文件、跳过日志和下载包）。
func copyTopLevel(src, dst string) []string {
	ents, err := os.ReadDir(src)
	if err != nil {
		return nil
	}
	var done []string
	for _, e := range ents {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		low := strings.ToLower(name)
		if strings.HasPrefix(low, "cfpilot.log") || strings.HasSuffix(low, ".exe") || strings.HasSuffix(low, ".old") {
			continue
		}
		to := filepath.Join(dst, name)
		if _, err := os.Stat(to); err == nil {
			continue
		}
		if err := copyFile(filepath.Join(src, name), to); err != nil {
			continue
		}
		done = append(done, name)
	}
	return done
}

func copyFile(from, to string) (err error) {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(to)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("复制 %s 失败：%w", from, err)
	}
	return nil
}
