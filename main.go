// Command icloud-hme 启动 iCloud Hide My Email 多账号管理平台。
//
// 两个核心 HTTP 接口:
//
//	POST /api/create  — 创建隐私邮箱别名
//	GET  /api/inbox   — 读取邮件
//
// 用法:
//
//	./icloud-hme                    # 默认 :8081
//	./icloud-hme -addr :9000        # 指定端口
//	./icloud-hme -data ./data       # 指定数据目录
//	./icloud-hme -debug             # 调试模式(等价于日志级别 debug)
//
// 安全配置(必填):
//
//	ICLOUD_HME_ADMIN_PASSWORD      管理员密码,至少 8 字符(进程启动后从环境清除)
//	ICLOUD_HME_SESSION_TTL         会话有效期,默认 12h,范围 15m-168h
//	ICLOUD_HME_SECURE_COOKIE       TLS 反向代理部署时设为 true
//
// 日志配置(可选):
//
//	ICLOUD_HME_LOG_LEVEL           日志级别 debug/info/warn/error(默认 info)
//	ICLOUD_HME_LOG_FORMAT          日志格式 text/json(默认 text)
//	ICLOUD_HME_LOG_DIR             日志持久化目录(默认 <数据目录>/logs;设为空串则只输出到 stderr)
package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/logging"
	"icloud-hme/internal/server"
)

func main() {
	addr := flag.String("addr", ":8081", "HTTP 监听地址")
	dataDir := flag.String("data", "./data", "数据目录 (accounts.json 存放位置)")
	debug := flag.Bool("debug", false, "调试模式 (启用 Gin 调试日志与 debug 级别日志)")
	flag.Parse()

	abs, err := filepath.Abs(*dataDir)
	if err != nil {
		log.Fatalf("数据目录路径错误: %v", err)
	}

	// 日志目录: 未设置 ICLOUD_HME_LOG_DIR 时默认落到 <数据目录>/logs(随 data 卷持久化);
	// 显式设为空串可关闭文件输出、仅保留 stderr。
	logDir := logging.DefaultDir(abs)
	if v, ok := os.LookupEnv("ICLOUD_HME_LOG_DIR"); ok {
		logDir = v
	}

	// 结构化日志: 级别、格式与持久化目录均可通过环境变量配置,便于线上排查。
	// 同时写 stderr(docker logs)与磁盘文件(按大小滚动,持久化到挂载卷)。
	_, closeLog, logErr := logging.Setup(logging.Config{
		Level:  os.Getenv("ICLOUD_HME_LOG_LEVEL"),
		Format: os.Getenv("ICLOUD_HME_LOG_FORMAT"),
		Debug:  *debug,
		Dir:    logDir,
	})
	defer func() { _ = closeLog() }()
	if logErr != nil {
		// 文件日志初始化失败已退化为仅 stderr;记录原因但不阻断启动。
		slog.Warn("文件日志初始化失败,已退化为仅 stderr 输出", "err", logErr.Error())
	} else if logDir != "" {
		slog.Info("日志持久化已启用", "dir", logDir)
	}

	adminPassword := os.Getenv("ICLOUD_HME_ADMIN_PASSWORD")
	if len(adminPassword) < 8 {
		log.Fatal("请通过环境变量 ICLOUD_HME_ADMIN_PASSWORD 设置管理员密码(至少 8 个字符)")
	}
	sessionTTL, err := parseSessionTTL(os.Getenv("ICLOUD_HME_SESSION_TTL"))
	if err != nil {
		log.Fatalf("ICLOUD_HME_SESSION_TTL 无效: %v", err)
	}
	secureCookie := os.Getenv("ICLOUD_HME_SECURE_COOKIE") == "true"

	slog.Info("服务启动中", "addr", *addr, "session_ttl", sessionTTL.String(), "secure_cookie", secureCookie)

	mgr, err := account.NewManager(abs)
	if err != nil {
		log.Fatalf("初始化账号管理器失败: %v", err)
	}
	defer mgr.Close()
	count := len(mgr.ListAccounts())
	slog.Info("账号加载完成", "count", count, "data_dir", abs)

	srv, err := server.New(mgr, server.Config{
		Debug:         *debug,
		AdminPassword: adminPassword,
		SessionTTL:    sessionTTL,
		SecureCookie:  secureCookie,
		AutoTaskFile:  filepath.Join(abs, "alias_task.json"),
	})
	if err != nil {
		log.Fatalf("初始化服务失败: %v", err)
	}

	// 密码只用于初始化认证,随后立即从进程环境清除
	_ = os.Unsetenv("ICLOUD_HME_ADMIN_PASSWORD")

	slog.Info("HTTP 服务就绪", "addr", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// parseSessionTTL 解析会话有效期,默认 12h,范围 15m-168h。
func parseSessionTTL(raw string) (time.Duration, error) {
	if raw == "" {
		return 12 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d < 15*time.Minute || d > 168*time.Hour {
		return 0, fmt.Errorf("会话有效期需在 15m 到 168h 之间，当前为 %v", d)
	}
	return d, nil
}
