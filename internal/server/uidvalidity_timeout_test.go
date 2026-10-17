package server

import (
	"testing"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

// ====================================================================
// IMAP 超时弃等路径: UIDVALIDITY 失效不得丢失
// ====================================================================
//
// 背景(缺陷): withIMAPTimeout 的超时只是「放弃等待」, 后台 goroutine 里的
// IMAP 读取仍会跑完。若把 UIDVALIDITY 失效留给外层(超时后读一个由后台
// goroutine 写的局部变量), 超时路径上 TakeUidValidityChange() 会被后台
// goroutine 消费掉, 而外层早已在超时点读过该变量(false)并跳过失效——
// preview 缓存(键为 folder:uid)在邮箱被重建后继续命中旧 UID, 展示/操作
// 错误邮件; 且该变量跨 goroutine 读写本身就是数据竞态。
//
// 契约: 失效动作必须在 WithMailClient 回调内、紧跟 TakeUidValidityChange
// 之后执行——只要后台 goroutine 观测到 UIDVALIDITY 变化, 无论外层是否已
// 超时返回, 该账号读缓存都必须失效(invalidateAccountReads 线程安全且幂等)。
//
// 时序(测试构造): 信封预算 = max(1*0.3s, floor=0.2s) = 0.3s < SELECT 延迟
// 0.8s, 外层在后台完成前必然超时返回; 后台读取跑完后执行失效, preview
// 缓存由命中转为未命中。修复前: 标记被后台消费而失效永不执行 → 轮询超时
// → FAIL。
func TestUidValidityTimeoutPathStillInvalidatesReadCaches(t *testing.T) {
	// 缩短信封预算下限, 让「预算 < SELECT 延迟」的确定性超时成立。
	prevFloor := imapEnvelopeBudgetFloor
	imapEnvelopeBudgetFloor = 200 * time.Millisecond
	t.Cleanup(func() { imapEnvelopeBudgetFloor = prevFloor })

	// 只扫 INBOX: 减少多文件夹 SELECT 次数(每次 SELECT 带 0.8s 延迟),
	// 让超时窗口与后台完成窗口的时序可控。
	prevFolders := mail.MailFolders
	mail.MailFolders = []string{"INBOX"}
	t.Cleanup(func() { mail.MailFolders = prevFolders })

	srv := mailtest.NewServer(t, mailtest.Options{
		UidValidity: 100,
		SelectDelay: 800 * time.Millisecond,
	})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))
	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetMailbox(sum.ID, account.MailboxConfig{
		Provider: "163", Email: "u@163.com", IMAPHost: srv.Host, IMAPPort: srv.Port, Password: "auth-code",
	}); err != nil {
		t.Fatal(err)
	}
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "验证码 123456", time.Now())

	b := &managerBackend{mgr: mgr}

	// 预热: 列表(Limit 20 → 预算 6s, 不会超时)拿邮件 ID, 再用摘要接口把
	// preview 缓存写热(键为 "preview:<账号>:<邮件ID>")。
	r, err := b.ListInbox(InboxQuery{AccountID: sum.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 1 {
		t.Fatalf("预热应 1 封, got %d", len(r.Messages))
	}
	id := r.Messages[0].ID
	if _, err := b.FetchPreviews(sum.ID, []string{id}); err != nil {
		t.Fatal(err)
	}
	previewKey := "preview:" + sum.ID + ":" + id
	if _, ok := b.respCache().get(previewKey); !ok {
		t.Fatalf("预热后 %s 应命中缓存", previewKey)
	}

	// 邮箱被服务端重建: UIDVALIDITY 100 → 200。
	srv.SetUidValidity(200)

	// 触发超时路径: Force 绕过全部缓存并作废 IMAP 快照, 预算 0.3s < SELECT
	// 延迟 0.8s → 外层先返回(降级 Web API; 本测试账号无 Cookie, 降级同样
	// 失败, 这不影响断言), 后台读取继续跑完并观测 UIDVALIDITY 变化。
	start := time.Now()
	_, _ = b.ListInbox(InboxQuery{AccountID: sum.ID, Limit: 1, Force: true})
	elapsed := time.Since(start)
	if elapsed >= 700*time.Millisecond {
		t.Fatalf("外层应走超时路径提前返回(< 700ms), 实际耗时 %v", elapsed)
	}

	// 后台读取完成时(约 2×SELECT 延迟后)必须已执行失效: 轮询等待 preview
	// 缓存由命中变未命中。修复前永不失效 → 5s 轮询超时 → FAIL。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := b.respCache().get(previewKey); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("超时弃等路径: 后台观测到 UIDVALIDITY 变化后 preview 缓存仍未失效(失效丢失)")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
