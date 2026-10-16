package server

import (
	"strings"
	"sync"
	"testing"
	"time"

	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

// ====================================================================
// 清理执行器: 分块节流删除的端到端行为
// ====================================================================
//
// 契约:
//   - 任务执行后邮件确实被删; 计数正确;
//   - 分块: N 封 / chunkSize 决定 STORE/EXPUNGE 次数;
//   - 块间节流被调用(可注入 sleep 观测);
//   - 失败重试 1 次, 仍失败 → paused(错误记录);
//   - 取消: 检查点生效, 不再继续。

// purgeHarness 组装: 真实 IMAP mock 服务器 + purge 管理器(注入客户端工厂)。
type purgeHarness struct {
	srv    *mailtest.Server
	mgr    *purgeManager
	client *mail.Client
	sleeps []time.Duration
}

func newPurgeHarness(t *testing.T, n int, opts mailtest.Options) *purgeHarness {
	t.Helper()
	srv := mailtest.NewServer(t, opts)
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))
	now := time.Now()
	for i := 0; i < n; i++ {
		srv.AddMessage(t, "INBOX", "a@example.com", "u@icloud.com",
			"旧邮件", now.AddDate(0, 0, -10).Add(time.Duration(i)*time.Second))
	}
	c := mail.NewClientWithServer("u@icloud.com", "app-password", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Disconnect)

	mgr, err := newPurgeManager("", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &purgeHarness{srv: srv, mgr: mgr, client: c}
	mgr.withClient = func(accountID string, fn func(*mail.Client) error) error {
		return fn(c)
	}
	mgr.sleep = func(d time.Duration) { h.sleeps = append(h.sleeps, d) }
	mgr.now = time.Now
	return h
}

// 端到端: 12 封(10 天前), 区间覆盖 → 全部删除, 计数正确。
func TestPurgeRunnerDeletesAll(t *testing.T) {
	h := newPurgeHarness(t, 12, mailtest.Options{})
	oldChunk := purgeChunkSize
	purgeChunkSize = 5 // 12/5 = 3 块
	t.Cleanup(func() { purgeChunkSize = oldChunk })

	tk, err := h.mgr.create("acc_1", "一", time.Now().AddDate(0, 0, -30), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.runSync(tk.ID)

	got, _ := h.mgr.get(tk.ID)
	if got.Status != "done" {
		t.Fatalf("状态 = %q, want done (err=%s)", got.Status, got.Error)
	}
	if got.Deleted != 12 || got.Processed != 12 {
		t.Fatalf("计数 = deleted %d / processed %d, want 12/12", got.Deleted, got.Processed)
	}
	// 命令预算: 3 块 → 3 STORE + 3 EXPUNGE。
	if s := h.srv.StoreCount(); s != 3 {
		t.Fatalf("STORE = %d, want 3", s)
	}
	if e := h.srv.ExpungeCount(); e != 3 {
		t.Fatalf("EXPUNGE = %d, want 3", e)
	}
	// 块间节流被调用(3 块 → 2 次块间等待)。
	interChunk := 0
	for _, d := range h.sleeps {
		if d >= purgeChunkDelay {
			interChunk++
		}
	}
	if interChunk != 2 {
		t.Fatalf("块间节流 = %d 次, want 2 (3 块之间)", interChunk)
	}
}

// 无邮件: 直接 done, 无 STORE。
func TestPurgeRunnerEmptyDone(t *testing.T) {
	h := newPurgeHarness(t, 0, mailtest.Options{})
	tk, _ := h.mgr.create("acc_1", "一", time.Now().AddDate(0, 0, -30), time.Now())
	h.mgr.runSync(tk.ID)
	got, _ := h.mgr.get(tk.ID)
	if got.Status != "done" {
		t.Fatalf("状态 = %q, want done", got.Status)
	}
	if got.Deleted != 0 {
		t.Fatalf("删除 = %d, want 0", got.Deleted)
	}
	if h.srv.StoreCount() != 0 {
		t.Fatal("空区间不应有 STORE")
	}
}

// 节流: 达到每小时块数上限时进入 throttled 并等待。
func TestPurgeRunnerThrottlesAtHourlyCap(t *testing.T) {
	h := newPurgeHarness(t, 25, mailtest.Options{})
	oldChunk, oldCap := purgeChunkSize, purgeHourlyChunkCap
	purgeChunkSize = 5      // 25/5 = 5 块
	purgeHourlyChunkCap = 3 // 第 4 块触发节流
	t.Cleanup(func() { purgeChunkSize, purgeHourlyChunkCap = oldChunk, oldCap })

	// 注入时钟: 推进时让节流窗口滚动。
	base := time.Now()
	var mu sync.Mutex
	cur := base
	h.mgr.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	h.mgr.sleep = func(d time.Duration) {
		mu.Lock()
		h.sleeps = append(h.sleeps, d)
		cur = cur.Add(d) // sleep 推进时钟 → 窗口滚动
		mu.Unlock()
	}

	tk, _ := h.mgr.create("acc_1", "一", base.AddDate(0, 0, -30), base)
	h.mgr.runSync(tk.ID)

	got, _ := h.mgr.get(tk.ID)
	if got.Status != "done" {
		t.Fatalf("状态 = %q, want done (err=%s)", got.Status, got.Error)
	}
	if got.Deleted != 25 {
		t.Fatalf("删除 = %d, want 25", got.Deleted)
	}
	// 应至少出现一次节流等待(≥ 到窗口滚动的时长)。
	throttled := false
	for _, d := range h.sleeps {
		if d >= 30*time.Second {
			throttled = true
		}
	}
	if !throttled {
		t.Fatalf("应触发节流等待, sleeps=%v", h.sleeps)
	}
}

// 取消: 检查点生效, 任务停止且状态 cancelled。
func TestPurgeRunnerCancelStops(t *testing.T) {
	h := newPurgeHarness(t, 30, mailtest.Options{})
	oldChunk := purgeChunkSize
	purgeChunkSize = 5
	t.Cleanup(func() { purgeChunkSize = oldChunk })

	tk, _ := h.mgr.create("acc_1", "一", time.Now().AddDate(0, 0, -30), time.Now())
	// 第一块完成后取消(通过 sleep 钩子在块间触发)。
	cancelled := false
	h.mgr.sleep = func(d time.Duration) {
		if !cancelled {
			cancelled = true
			_ = h.mgr.cancel(tk.ID)
		}
	}
	h.mgr.runSync(tk.ID)

	got, _ := h.mgr.get(tk.ID)
	if got.Status != "cancelled" {
		t.Fatalf("状态 = %q, want cancelled", got.Status)
	}
	// 不应删完全部(30 封, 至少留下后面几块)。
	if got.Deleted >= 30 {
		t.Fatalf("取消后不应删完: deleted=%d", got.Deleted)
	}
}

// 失败: 客户端工厂报错 → paused 且记录错误。
func TestPurgeRunnerFailurePauses(t *testing.T) {
	h := newPurgeHarness(t, 10, mailtest.Options{})
	h.mgr.withClient = func(accountID string, fn func(*mail.Client) error) error {
		return errTestPurge
	}
	tk, _ := h.mgr.create("acc_1", "一", time.Now().AddDate(0, 0, -30), time.Now())
	h.mgr.runSync(tk.ID)

	got, _ := h.mgr.get(tk.ID)
	if got.Status != "paused" {
		t.Fatalf("状态 = %q, want paused", got.Status)
	}
	if got.Error == "" {
		t.Fatal("应记录错误")
	}
	if !strings.Contains(got.Error, "搜索") {
		t.Fatalf("错误应指明阶段: %s", got.Error)
	}
}

var errTestPurge = errPurgeTest("模拟 IMAP 故障")

type errPurgeTest string

func (e errPurgeTest) Error() string { return string(e) }
