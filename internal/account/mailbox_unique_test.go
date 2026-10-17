package account

import (
	"errors"
	"testing"
)

// 同一外部收件邮箱不得绑定到两个账号。
//
// 审计 B1: 邮箱池按 host:port:email 复用连接, 两个账号共享同一物理邮箱时,
// 一个账号的受限 API 秘钥可以读到/删除另一个账号别名的邮件。归属校验发生在
// IMAP 连接之前, 因此这里用不可达主机也能命中 ErrMailboxInUse。
func TestSetMailboxRejectsDuplicateEmail(t *testing.T) {
	m := &Manager{accounts: map[string]*Account{
		"acc_1": {ID: "acc_1", Mailbox: &MailboxConfig{
			Email: "Shared@Example.com", IMAPHost: "imap.example.com", IMAPPort: 993,
		}},
		"acc_2": {ID: "acc_2"},
	}}

	err := m.SetMailbox("acc_2", MailboxConfig{
		Email: "shared@example.com", IMAPHost: "imap.example.com", IMAPPort: 993, Password: "x",
	})
	if !errors.Is(err, ErrMailboxInUse) {
		t.Fatalf("重复邮箱应返回 ErrMailboxInUse, got %v", err)
	}
}

// 同一账号重复保存自己的邮箱不算冲突(否则无法更新授权码)。
func TestSetMailboxAllowsSameAccountReSave(t *testing.T) {
	m := &Manager{accounts: map[string]*Account{
		"acc_1": {ID: "acc_1", Mailbox: &MailboxConfig{
			Email: "me@example.com", IMAPHost: "127.0.0.1", IMAPPort: 1,
		}},
	}}

	// 端口 1 不可达 → 后续 IMAP 校验必然失败, 但绝不能是 ErrMailboxInUse。
	err := m.SetMailbox("acc_1", MailboxConfig{
		Email: "me@example.com", IMAPHost: "127.0.0.1", IMAPPort: 1, Password: "x",
	})
	if errors.Is(err, ErrMailboxInUse) {
		t.Fatal("同账号重复保存不应报 MAILBOX_IN_USE")
	}
}
