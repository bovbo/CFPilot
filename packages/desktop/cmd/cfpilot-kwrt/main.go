// CFPilot 路由器版（Kwrt / OpenWrt）入口。
//
// ## 与桌面版的区别
//
// 同一份服务端、同一份界面、同一份内核，只是入口不同：
//   - 不引入托盘（systray / dbus）、桌面通知、自动打开浏览器 —— 编译阶段就没有这些代码；
//   - 默认监听所有网卡并允许局域网访问（路由器就是用来在局域网里访问的）；
//   - 启动令牌地址写入文件（默认 /tmp/cfpilot.url，权限 0600），不进持久日志；
//   - 由 procd 守护，前台运行，收到 SIGTERM 干净退出。
//
// ## 启动参数
//
//	--port <n>        覆盖监听端口（仅本次生效）
//	--data-dir <dir>  数据目录（凭据、配置、日志），等价于环境变量 CFPILOT_DATA_DIR
//	--url-file <path> 把带令牌的管理地址写到这个文件（默认 /tmp/cfpilot.url，空串 = 不写）
//	--loopback        只监听 127.0.0.1（不开放给局域网）
//	--check           只做自检
//	--version         打印版本号
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/xmwengxing/cfpilot/packages/desktop/internal/config"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/kernel"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/logfile"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/scan"
	"github.com/xmwengxing/cfpilot/packages/desktop/internal/server"
)

// Version 由构建时通过 -ldflags 注入。
var Version = "dev"

func main() {
	// 只用内置内核（含多数据面接口），不换成上游发布的远程内核
	kernel.DisableRemote = true
	var (
		portFlag    = flag.Int("port", 0, "监听端口（覆盖配置，仅本次生效）")
		dataDir     = flag.String("data-dir", "", "数据目录（凭据 / 配置 / 日志）")
		logMaxKB    = flag.Int("log-max-kb", 512, "日志单文件上限（KB）；路由器闪存小，默认 512")
		logBackups  = flag.Int("log-backups", 1, "保留的历史日志文件个数")
		noLimit     = flag.Bool("no-limit", false, "关闭路由器档并发限流（小路由不建议）")
		urlFile     = flag.String("url-file", "/tmp/cfpilot.url", "把带令牌的管理地址写到该文件，空串 = 不写")
		loopback    = flag.Bool("loopback", false, "只监听 127.0.0.1，不开放局域网")
		checkOnly   = flag.Bool("check", false, "只做自检")
		versionFlag = flag.Bool("version", false, "打印版本号")
	)
	flag.Parse()

	if *versionFlag {
		fmt.Printf("CFPilot Kwrt %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	if *dataDir != "" {
		_ = os.Setenv(config.EnvDataDir, *dataDir)
	}
	// 路由器版默认对局域网开放；显式 --loopback 才收回。
	// 环境变量由调用方（init 脚本）显式给出时尊重其取值。
	if !*loopback {
		if os.Getenv(config.EnvListenAddr) == "" {
			_ = os.Setenv(config.EnvListenAddr, config.AnyHost)
		}
		if os.Getenv(config.EnvAllowLAN) == "" {
			_ = os.Setenv(config.EnvAllowLAN, "1")
		}
	}

	// 日志：始终写文件（界面「日志」页读它），同时输出到 stderr 交给 procd / logread。
	if dir, err := config.EnsureDir(); err == nil {
		maxBytes := int64(*logMaxKB) * 1024
		if maxBytes < 64*1024 {
			maxBytes = 64 * 1024
		}
		backups := *logBackups
		if backups < 0 {
			backups = 0
		}
		w, werr := logfile.New(filepath.Join(dir, "cfpilot.log"), maxBytes, backups)
		if werr == nil {
			log.SetOutput(io.MultiWriter(w, os.Stderr))
			defer func() { _ = w.Close() }()
			redirectStderrToLog(w)
		} else {
			log.Printf("[cfpilot] 打开日志文件失败：%v", werr)
		}
	}

	if _, err := config.EnsureDir(); err != nil {
		fatal(err)
	}

	settings, err := config.LoadSettings()
	if err != nil {
		log.Printf("[cfpilot] 读取设置失败，使用默认值：%v", err)
	}
	if *portFlag > 0 {
		if *portFlag > 65535 {
			fatal(fmt.Errorf("端口 %d 超出范围（1-65535）", *portFlag))
		}
		settings.Port = *portFlag
	} else if p := config.EnvPortValue(); p > 0 {
		settings.Port = p
	}
	if !*loopback && !settings.AllowLAN {
		settings.AllowLAN = true
	}
	settings.Host = settings.EffectiveHost()

	if *checkOnly {
		fmt.Printf("配置目录：%s\n", config.SettingsPathForDisplay())
		fmt.Printf("监听地址：%s:%d\n", settings.Host, settings.Port)
		fmt.Printf("版本：%s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	// 路由器档：压低扫描并发 + 降低进程优先级，避免优选时把路由器卡死。
	if !*noLimit {
		scan.SetRouterLimits(scan.DefaultRouterLimits())
		// nice=10：转发、DHCP、DNS 等路由本职工作优先于优选
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10); err != nil {
			log.Printf("[cfpilot] 降低进程优先级失败（可忽略）：%v", err)
		}
		log.Printf("[cfpilot] 已启用路由器档限流：TCP 并发≤%d，TLS≤%d，探测≤%d，测速连接≤%d",
			scan.DefaultRouterLimits().Concurrency, scan.DefaultRouterLimits().TLSConcurrency,
			scan.DefaultRouterLimits().ProbeConcurrency, scan.DefaultRouterLimits().SpeedConnections)
	}

	server.SetVersion(Version)
	srv, err := server.New(settings)
	if err != nil {
		fatal(err)
	}
	if err := srv.Start(); err != nil {
		fatal(fmt.Errorf("%w\n提示：用 --port <端口> 换一个，或先关掉占用该端口的程序", err))
	}

	log.Printf("[cfpilot] 路由器版已启动 %s，监听 %s:%d", Version, settings.Host, settings.Port)
	if !*loopback {
		log.Printf("[cfpilot] ⚠️ 已对局域网开放；面板持有 Cloudflare Token 的读写权限，请勿把该端口暴露到公网")
	}

	// 带令牌的地址只写文件（0600），不进持久日志。
	if *urlFile != "" {
		u := lanURL(srv, settings)
		if err := os.WriteFile(*urlFile, []byte(u+"\n"), 0o600); err != nil {
			log.Printf("[cfpilot] 写入地址文件失败：%v", err)
		} else {
			log.Printf("[cfpilot] 管理地址（含一次性令牌）已写入 %s", *urlFile)
		}
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	log.Printf("[cfpilot] 收到退出信号，正在停止")
	_ = srv.Stop()
}

// lanURL 把欢迎链接里的 127.0.0.1 换成路由器的局域网地址，便于从电脑浏览器直接打开。
func lanURL(srv *server.Server, s config.DesktopSettings) string {
	host := s.Host
	if host == "" || host == config.AnyHost || host == "::" {
		host = lanIP()
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.Port)) + "/?t=" + srv.BootToken()
}

// lanIP 优先取 br-lan 的 IPv4，其次取第一个私有 IPv4，都没有就退回 127.0.0.1。
func lanIP() string {
	pick := func(ifc net.Interface) string {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && ip4.IsPrivate() {
					return ip4.String()
				}
			}
		}
		return ""
	}
	if ifc, err := net.InterfaceByName("br-lan"); err == nil {
		if ip := pick(*ifc); ip != "" {
			return ip
		}
	}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if ip := pick(ifc); ip != "" {
			return ip
		}
	}
	return config.LoopbackHost
}

// redirectStderrToLog 把 stderr 接到日志文件（panic / runtime 致命错误也能留下现场）。
//
// ⚠️ 只能有一个读者且不能用 io.Copy（会走 splice 长期占住 stderr 写锁导致整个程序卡死），
// 原因与细节见桌面版 cmd/cfpilot/main.go 同名函数的注释。这里保持同样的 TeeReader 写法。
func redirectStderrToLog(w io.Writer) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return
	}
	orig := os.Stderr
	os.Stderr = pw
	go func() {
		defer func() { _ = recover() }()
		defer pr.Close()
		sc := bufio.NewScanner(io.TeeReader(pr, orig))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			_, _ = fmt.Fprintf(w, "[stderr] %s\n", sc.Text())
		}
	}()
}

func fatal(err error) {
	log.Printf("[cfpilot] %v", err)
	os.Exit(1)
}

var _ = strings.TrimSpace
