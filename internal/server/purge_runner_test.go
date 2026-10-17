package server

import (
	"strings"
	"sync"
	"sync/atomic"
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

// 单飞: 同一任务同时只允许 1 个 runner(双 runner 会让命令数翻倍)。
func TestPurgeRunIsSingleFlightPerTask(t *testing.T) {
	h := newPurgeHarness(t, 4, mailtest.Options{})

	entered := make(chan struct{}) // 第一个 runner 进入 withClient 后关闭
	release := make(chan struct{}) // 测试放行
	var calls atomic.Int32         // withClient 调用总数
	var post atomic.Int32          // 放行并复位后, 新 runner 的 withClient 调用数
	var trackPost atomic.Bool
	var blockOnce sync.Once
	h.mgr.withClient = func(accountID string, fn func(*mail.Client) error) error {
		calls.Add(1)
		if trackPost.Load() {
			post.Add(1)
		}
		blockOnce.Do(func() {
			close(entered)
			<-release // 第一个 runner 停在这里(其它 runner 若存在会直接越过)
		})
		return fn(h.client)
	}

	tk, err := h.mgr.create("acc_1", "一", time.Now().AddDate(0, 0, -30), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.run(tk.ID)
	<-entered // 第 1 个 runner 已进入

	// 同 ID 再 run: 必须被单飞守卫拒绝(不得起第 2 个 runner)。
	h.mgr.run(tk.ID)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("第二次 run 不应进入 runner: withClient 调用 = %d, want 1", got)
	}

	close(release)
	// 等第 1 个 runner 退出(单飞位已清)。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mgr.mu.Lock()
		running := h.mgr.running[tk.ID]
		h.mgr.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mgr.mu.Lock()
	stillRunning := h.mgr.running[tk.ID]
	h.mgr.mu.Unlock()
	if stillRunning {
		t.Fatal("第 1 个 runner 应已退出并清位")
	}

	// 放行退出后再 run: 能进入(第 2 次 runner 进入)。
	trackPost.Store(true)
	h.mgr.run(tk.ID)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && post.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if post.Load() == 0 {
		t.Fatal("放行后 run 应能进入 runner(第 2 次进入)")
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("两次 runner 进入的调用计数 = %d, want ≥ 2", got)
	}
}

// 节流等待期间暂停: 醒来不得覆盖为 running, 且不再删除。
func TestPurgeThrottleWaitRespectsPause(t *testing.T) {
	h := newPurgeHarness(t, 25, mailtest.Options{})
	oldChunk, oldCap := purgeChunkSize, purgeHourlyChunkCap
	purgeChunkSize = 5      // 25/5 = 5 块
	purgeHourlyChunkCap = 3 // 第 4 块触发节流
	t.Cleanup(func() { purgeChunkSize, purgeHourlyChunkCap = oldChunk, oldCap })

	tk, err := h.mgr.create("acc_1", "一", time.Now().AddDate(0, 0, -30), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 节流等待(sleep)期间用户点「暂停」: 模拟 sleep 中调 pause。
	storesAtPause := int64(0)
	h.mgr.sleep = func(d time.Duration) {
		h.sleeps = append(h.sleeps, d)
		if d >= 30*time.Second { // 节流等待(远大于块间 3~5s)
			storesAtPause = h.srv.StoreCount()
			if err := h.mgr.pause(tk.ID); err != nil {
				t.Errorf("pause: %v", err)
			}
		}
	}
	h.mgr.runSync(tk.ID)

	got, _ := h.mgr.get(tk.ID)
	if got.Status != "paused" {
		t.Fatalf("节流期间暂停被吞: 状态 = %q, want paused", got.Status)
	}
	if now := h.srv.StoreCount(); now != storesAtPause {
		t.Fatalf("暂停后不应继续删除: STORE %d → %d", storesAtPause, now)
	}
	if got.Deleted != 15 { // 3 块已删, 第 4 块未执行
		t.Fatalf("已删 = %d, want 15(3 块)", got.Deleted)
	}
}

// 块删除重试后仍失败: Failed 计入该块邮件数且 status=paused。
func TestPurgeFailedCountOnChunkFailure(t *testing.T) {
	h := newPurgeHarness(t, 7, mailtest.Options{})
	oldChunk := purgeChunkSize
	purgeChunkSize = 3 // 7 封 → 首块 3 封
	t.Cleanup(func() { purgeChunkSize = oldChunk })

	// 首次 withClient 调用是 SEARCH(放行真实客户端); 之后的调用是
	// DeleteUIDChunk(重试 2 次均失败)。
	var calls atomic.Int32
	h.mgr.withClient = func(accountID string, fn func(*mail.Client) error) error {
		if calls.Add(1) == 1 {
			return fn(h.client)
		}
		return errTestPurge
	}
	h.mgr.sleep = func(d time.Duration) { h.sleeps = append(h.sleeps, d) }

	tk, err := h.mgr.create("acc_1", "一", time.Now().AddDate(0, 0, -30), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.runSync(tk.ID)

	got, _ := h.mgr.get(tk.ID)
	if got.Status != "paused" {
		t.Fatalf("状态 = %q, want paused", got.Status)
	}
	if got.Failed != 3 {
		t.Fatalf("Failed = %d, want 3(首块邮件数)", got.Failed)
	}
	if got.Error == "" {
		t.Fatal("应记录错误")
	}
	if got.Deleted != 0 {
		t.Fatalf("失败块不应计入已删: deleted = %d", got.Deleted)
	}
}

// 每块删除成功后失效账号读缓存。
func TestPurgeInvalidatesReadCachesAfterChunk(t *testing.T) {
	h := newPurgeHarness(t, 6, mailtest.Options{})
	oldChunk := purgeChunkSize
	purgeChunkSize = 2 // 3 块
	t.Cleanup(func() { purgeChunkSize = oldChunk })

	var mu sync.Mutex
	var accounts []string
	h.mgr.invalidateReads = func(accountID string) {
		mu.Lock()
		accounts = append(accounts, accountID)
		mu.Unlock()
	}

	tk, err := h.mgr.create("acc_1", "一", time.Now().AddDate(0, 0, -30), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.runSync(tk.ID)

	got, _ := h.mgr.get(tk.ID)
	if got.Status != "done" || got.Deleted != 6 {
		t.Fatalf("状态 = %q deleted = %d, want done/6", got.Status, got.Deleted)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(accounts) < 1 {
		t.Fatalf("每块删除后应失效读缓存, 调用 = %d", len(accounts))
	}
	for _, a := range accounts {
		if a != "acc_1" {
			t.Fatalf("失效账号应为 acc_1, got %q", a)
		}
	}
}

// IMAP 操作超时: SearchRange 卡死超预算 → paused 且错误含超时信息。
func TestPurgeSearchTimeoutPauses(t *testing.T) {
	h := newPurgeHarness(t, 3, mailtest.Options{})
	oldTimeout := purgeOpTimeout
	purgeOpTimeout = 30 * time.Millisecond
	t.Cleanup(func() { purgeOpTimeout = oldTimeout })

	// withClient 阻塞远超预算(模拟 IMAP 卡死)。
	h.mgr.withClient = func(accountID string, fn func(*mail.Client) error) error {
		time.Sleep(2 * time.Second)
		return nil
	}

	tk, err := h.mgr.create("acc_1", "一", time.Now().AddDate(0, 0, -30), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	h.mgr.runSync(tk.ID)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("超时应及时返回, 实际 %v", elapsed)
	}

	got, _ := h.mgr.get(tk.ID)
	if got.Status != "paused" {
		t.Fatalf("状态 = %q, want paused", got.Status)
	}
	if !strings.Contains(got.Error, "超时") {
		t.Fatalf("错误应含超时信息: %q", got.Error)
	}
}
