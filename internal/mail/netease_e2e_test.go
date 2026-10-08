package mail

import (
	"os"
	"testing"
)

// TestE2ENetEase163IDRequired 连接真实 163 IMAP 服务器, 验证 RFC 2971 ID 命令修复。
//
// 网易 163/126(Coremail) 在 LOGIN 后强制要求客户端先发送非空 ID 声明身份,
// 否则后续 SELECT 一律返回 "Unsafe Login. Please contact kefu@188.com for help"。
// 本测试默认跳过, 需真实凭据(授权码, 非登录密码):
//
//	ICLOUD_HME_E2E_163_USER=xxx@163.com ICLOUD_HME_E2E_163_CODE=授权码 \
//	  go test ./internal/mail -run TestE2ENetEase163 -v
func TestE2ENetEase163IDRequired(t *testing.T) {
	user := os.Getenv("ICLOUD_HME_E2E_163_USER")
	code := os.Getenv("ICLOUD_HME_E2E_163_CODE")
	if user == "" || code == "" {
		t.Skip("未设置 ICLOUD_HME_E2E_163_USER / ICLOUD_HME_E2E_163_CODE, 跳过真实 163 验证")
	}
	c := NewClientWithServer(user, code, "imap.163.com", 993)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect(163): %v", err)
	}
	defer c.Disconnect()
	// SELECT INBOX 是「接入收件邮箱」验证路径(SetMailbox → InboxCount)的核心步骤;
	// 未发送 ID 的实现会在这里收到 Unsafe Login 并导致 502。
	count, err := c.InboxCount()
	if err != nil {
		t.Fatalf("InboxCount(163, 需要 ID 命令): %v", err)
	}
	t.Logf("163 收件箱共 %d 封邮件", count)

	// 列表读取同时扫描 INBOX + Junk + 「垃圾邮件」: 网易无 Junk 文件夹,
	// 且缺失文件夹错误措辞为 "Folder not exist", 两处都必须正确处理。
	msgs, total, err := c.ListInboxPageRange(5, 0, DateRange{})
	if err != nil {
		t.Fatalf("163 列表读取失败(垃圾邮件夹扫描): %v", err)
	}
	t.Logf("163 列表读取: %d/%d", len(msgs), total)
}
