package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{" Error ", slog.LevelError},
		{"nonsense", slog.LevelInfo},
	}
	for _, tc := range cases {
		if got := ParseLevel(tc.in); got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSetupReturnsLoggerAndSetsDefault(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		enable slog.Level // 应启用的级别
		reject slog.Level // 应被过滤的级别
	}{
		{"默认 info", Config{}, slog.LevelInfo, slog.LevelDebug},
		{"json 格式 warn", Config{Level: "warn", Format: "json"}, slog.LevelWarn, slog.LevelInfo},
		{"debug 标志覆盖级别", Config{Level: "error", Debug: true}, slog.LevelDebug, -100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, closer, err := Setup(tc.cfg)
			if err != nil {
				t.Fatalf("Setup 返回错误: %v", err)
			}
			defer func() { _ = closer() }()
			if logger == nil {
				t.Fatal("Setup 返回 nil")
			}
			if slog.Default() != logger {
				t.Fatal("Setup 未将 logger 设为全局默认")
			}
			if !logger.Enabled(nil, tc.enable) {
				t.Errorf("级别 %v 应被启用", tc.enable)
			}
			if tc.reject != -100 && logger.Enabled(nil, tc.reject) {
				t.Errorf("级别 %v 应被过滤", tc.reject)
			}
		})
	}
}

// TestSetupWritesToFile 验证配置日志目录后日志会落盘。
func TestSetupWritesToFile(t *testing.T) {
	dir := t.TempDir()
	logger, closer, err := Setup(Config{Level: "info", Format: "json", Dir: dir})
	if err != nil {
		t.Fatalf("Setup 返回错误: %v", err)
	}
	logger.Info("落盘测试", "k", "v")
	if err := closer(); err != nil {
		t.Fatalf("closer 返回错误: %v", err)
	}

	path := filepath.Join(dir, defaultLogFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	if !strings.Contains(string(data), "落盘测试") || !strings.Contains(string(data), `"k":"v"`) {
		t.Fatalf("日志文件内容不含预期记录: %s", data)
	}
}

// TestRotation 验证写满后按大小滚动并保留历史文件。
func TestRotation(t *testing.T) {
	dir := t.TempDir()
	rw, err := newRotatingWriter(dir, "app.log", 100, 2)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer rw.Close()

	// 每条 40 字节,写 6 条,必然触发多次滚动(上限 100 字节)。
	line := make([]byte, 40)
	for i := range line {
		line[i] = 'x'
	}
	for i := 0; i < 6; i++ {
		if _, err := rw.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// 至少应生成 app.log 与 app.log.1;最旧的 app.log.3 不应存在(maxBackups=2)。
	if _, err := os.Stat(filepath.Join(dir, "app.log")); err != nil {
		t.Fatalf("app.log 应存在: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.log.1")); err != nil {
		t.Fatalf("app.log.1 应存在(滚动后的历史): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.log.3")); err == nil {
		t.Fatal("app.log.3 不应存在(超过 maxBackups=2)")
	}
}

// TestDefaultDir 验证默认日志目录拼接。
func TestDefaultDir(t *testing.T) {
	if got := DefaultDir("/app/data"); got != filepath.Join("/app/data", "logs") {
		t.Fatalf("DefaultDir = %q", got)
	}
}
