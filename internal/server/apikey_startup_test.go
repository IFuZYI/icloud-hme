package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"icloud-hme/internal/account"
)

// ====================================================================
// 秘钥文件加载失败: 构造必须报错, 不得留下 nil manager(请求时 panic)
// ====================================================================
//
// 回归背景(审查探针复现): apikey.NewManager 对损坏的 api_keys.json 返回
// (nil, err); server 构造路径曾丢弃该错误 → s.keys == nil → 任何带
// X-API-Key 的请求都在中间件里 nil 解引用(500, 由 Recovery 兜住)。
//
// 契约: 损坏文件让构造失败并给出可读原因; 生产路径(main.go)据此拒绝启动,
// 与损坏的 accounts.json 同语义(不静默丢数据、不带着半残状态运行)。

func TestServerNewFailsOnCorruptAPIKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "api_keys.json")
	if err := os.WriteFile(keyFile, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr, err := account.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)

	_, err = New(mgr, Config{
		AdminPassword: "admin-pass-2026-strong",
		AutoTaskFile:  filepath.Join(dir, "alias_task.json"),
		APIKeyFile:    keyFile,
	})
	if err == nil {
		t.Fatal("损坏的 api_keys.json 应让 New 报错(否则 nil manager 在首个秘钥请求时 panic)")
	}
	if !strings.Contains(err.Error(), "秘钥") {
		t.Fatalf("错误信息应说明是秘钥文件问题: %v", err)
	}
}

// newWithBackendE 是构造的唯一出口, 同样必须报错(两条路径都不能漏)。
func TestNewWithBackendERejectsCorruptKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "api_keys.json")
	// 形状错误(数组而非对象): JSON 语法合法但结构不符, 同样必须拒绝。
	if err := os.WriteFile(keyFile, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newWithBackendE(&fakeBackend{}, Config{
		AdminPassword: "admin-pass-2026-strong",
		AutoTaskFile:  filepath.Join(dir, "alias_task.json"),
		APIKeyFile:    keyFile,
	})
	if err == nil {
		t.Fatal("结构错误的 api_keys.json 应让构造报错")
	}
}
