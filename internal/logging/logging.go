// Package logging 提供基于 log/slog 的结构化日志初始化。
//
// 日志级别、输出格式与持久化目录均可通过环境变量或命令行标志配置,便于线上排查:
//
//	ICLOUD_HME_LOG_LEVEL   日志级别 debug/info/warn/error(默认 info)
//	ICLOUD_HME_LOG_FORMAT  日志格式 text/json(默认 text)
//	ICLOUD_HME_LOG_DIR     日志持久化目录(默认 <数据目录>/logs;空字符串表示仅输出到 stderr)
//
// 调用 Setup 后会设置为 slog 的全局默认 Logger,进程内任意位置可直接使用
// slog.Debug/Info/Warn/Error 记录日志。日志同时写入 stderr(供 docker logs 采集)
// 与磁盘文件(按大小滚动,持久化到挂载卷),两条路径的内容完全一致。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	// defaultLogFileName 是滚动日志的主文件名。
	defaultLogFileName = "app.log"
	// defaultMaxSizeMB 是单个日志文件的大小上限(MiB),超过后滚动。
	defaultMaxSizeMB = 10
	// defaultMaxBackups 是保留的历史日志文件数量(app.log.1 … app.log.N)。
	defaultMaxBackups = 5
)

// Config 是日志初始化配置。字段为空时回退到默认值。
type Config struct {
	// Level 为日志级别:debug/info/warn/error。为空取 info。
	Level string
	// Format 为输出格式:text 或 json。为空取 text。
	Format string
	// Debug 为 true 时强制使用 debug 级别(对应 -debug 标志),覆盖 Level。
	Debug bool
	// Dir 为日志持久化目录。为空则只输出到 stderr,不落盘。
	Dir string
	// FileName 为日志文件名,为空取 app.log。
	FileName string
	// MaxSizeMB 为单文件大小上限(MiB),<=0 取默认值。
	MaxSizeMB int
	// MaxBackups 为保留的历史文件数,<0 取默认值,0 表示不保留历史(只截断当前文件)。
	MaxBackups int
}

// Setup 依据 cfg 构造 slog.Logger,设置为全局默认并返回。
//
// 返回值 closer 用于在进程退出时关闭底层日志文件(未开启文件输出时为 no-op);
// 调用方应 defer closer()。当 Dir 非空但目录创建或文件打开失败时返回 error,
// 但仍会返回一个可用的、仅输出到 stderr 的 Logger,保证服务不因日志落盘失败而无法启动。
func Setup(cfg Config) (logger *slog.Logger, closer func() error, err error) {
	level := ParseLevel(cfg.Level)
	if cfg.Debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}

	closer = func() error { return nil }
	var writer io.Writer = os.Stderr

	if strings.TrimSpace(cfg.Dir) != "" {
		fileName := cfg.FileName
		if fileName == "" {
			fileName = defaultLogFileName
		}
		maxSize := cfg.MaxSizeMB
		if maxSize <= 0 {
			maxSize = defaultMaxSizeMB
		}
		maxBackups := cfg.MaxBackups
		if maxBackups < 0 {
			maxBackups = defaultMaxBackups
		}
		rw, openErr := newRotatingWriter(cfg.Dir, fileName, int64(maxSize)*1024*1024, maxBackups)
		if openErr != nil {
			// 落盘失败不致命:退化为仅 stderr,并把原因作为 error 返回给调用方记录。
			err = fmt.Errorf("初始化文件日志失败(将仅输出到 stderr): %w", openErr)
		} else {
			// 同时写 stderr 与文件:docker logs 与持久化文件内容一致。
			writer = io.MultiWriter(os.Stderr, rw)
			closer = rw.Close
		}
	}

	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "json":
		handler = slog.NewJSONHandler(writer, opts)
	default:
		handler = slog.NewTextHandler(writer, opts)
	}

	logger = slog.New(handler)
	slog.SetDefault(logger)
	return logger, closer, err
}

// ParseLevel 把字符串解析为 slog.Level,无法识别时返回 info。
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// DefaultDir 返回数据目录下的默认日志目录 <dataDir>/logs。
func DefaultDir(dataDir string) string {
	return filepath.Join(dataDir, "logs")
}
