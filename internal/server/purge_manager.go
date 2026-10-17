// Package server - 邮件清理任务管理器。
//
// 一次清理 = 一条 PurgeTask(无论多少封邮件), 异步执行:
//   - 按账号并发: 同账号同时最多 1 个任务, 不同账号可并行;
//   - 每个任务同时最多 1 个 runner(run 单飞; 双 runner 会让 IMAP 命令数
//     近乎翻倍, 击穿分块节流的风控设计);
//   - 分块执行(每块 chunkSize 封), 块间节流 + 抖动, 每小时块数封顶;
//   - 断点续传: 恢复时重新 SEARCH(已删 UID 不再返回), ChunkCursor 仅作展示;
//   - 持久化 purge_tasks.json(原子写; 活跃任务无条件保留且不计入上限,
//     终态任务保留最近 50 条), 重启后运行中任务标记 interrupted(可 resume)。
package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"icloud-hme/internal/apikey"
	"icloud-hme/internal/mail"
)

// ====================================================================
// 参数(可注入, 测试覆盖)
// ====================================================================

var (
	// purgeChunkSize 是每块的邮件数。
	purgeChunkSize = 500
	// purgeChunkDelay 是块间基础间隔; 实际间隔再加 0~purgeChunkJitter 的抖动。
	purgeChunkDelay = 3 * time.Second
	// purgeChunkJitter 是块间抖动上限(随机 0~该值), 避免固定节奏特征。
	purgeChunkJitter = 2 * time.Second
	// purgeHourlyChunkCap 是滚动 1 小时内最多执行的块数; 达到后转 throttled。
	purgeHourlyChunkCap = 20
	// purgeKeepTasks 是保留的终态任务记录条数(活跃任务不占额度)。
	purgeKeepTasks = 50
	// purgeOpTimeout 是 purge 的 IMAP 操作预算(单次 SEARCH/删除块的等待上限);
	// 没有它时卡死的 IMAP 会让任务永停 running(账号锁不释放)、预览 HTTP 挂死。
	// 测试可缩短。
	purgeOpTimeout = 60 * time.Second
	// purgeFolders 是清理覆盖的文件夹(与读信口径一致)。
	purgeFolders = []string{"INBOX", "Junk", "垃圾邮件"}
)

// ====================================================================
// 数据模型
// ====================================================================

// PurgeTask 是一次邮件清理任务的完整状态。
type PurgeTask struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name,omitempty"`
	// CreatedByKeyID 是创建任务的 API 秘钥 ID; 管理员会话创建时为空。
	// 执行期据此重校权限(秘钥删除/停用/缩权/缩范围 → 暂停任务)。
	CreatedByKeyID string `json:"created_by_key_id,omitempty"`
	RangeStart     string `json:"range_start"`
	RangeEnd       string `json:"range_end"`
	// Status: running|throttled|paused|done|failed|cancelled|interrupted
	Status string `json:"status"`
	// PauseReason 是机器可读的暂停原因(如 key_revoked); 普通暂停为空。
	// 权限失效类原因会拒绝 resume(任务必须用新秘钥重建)。
	PauseReason string `json:"pause_reason,omitempty"`
	// Total 是预览确定的待删总数; Processed 是已处理数。
	Total     int `json:"total"`
	Processed int `json:"processed"`
	Deleted   int `json:"deleted"`
	Failed    int `json:"failed"`
	// CurrentMessage 是当前块的描述(如 "INBOX · 500 封")。
	CurrentMessage string `json:"current_message,omitempty"`
	// 断点: 当前处理到的文件夹与块序号(块级断点)。
	FolderCursor string `json:"folder_cursor,omitempty"`
	ChunkCursor  int    `json:"chunk_cursor,omitempty"`
	Chunks       int    `json:"chunks,omitempty"`
	// HourlyChunks 记录最近 1 小时内的块时刻(节流窗口判定)。
	HourlyChunks []string `json:"hourly_chunks,omitempty"`
	StartedAt    string   `json:"started_at"`
	FinishedAt   string   `json:"finished_at,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// 暂停原因(机器可读): 由执行期秘钥校验器注入, 用于区分「权限失效」与
// 普通失败暂停——前者一律拒绝 resume。
const (
	purgePauseReasonKeyRevoked           = "key_revoked"
	purgePauseReasonKeyPermissionRevoked = "key_permission_revoked"
	purgePauseReasonKeyScopeRevoked      = "key_scope_revoked"
)

// purgeResumeDeniedMessage 是「因权限失效暂停」的任务 resume 被拒时的
// 用户可见文案(handler 409 与 manager 哨兵错误共用)。
const purgeResumeDeniedMessage = "创建该任务的 API 秘钥已失效或权限已变更，请用新秘钥重新创建清理任务"

// ErrResumeDeniedByPolicy 表示任务因创建者秘钥权限失效而暂停, 不允许
// resume——继续执行等于绕过秘钥吊销, 必须用有效秘钥重建任务。
var ErrResumeDeniedByPolicy = errors.New(purgeResumeDeniedMessage)

// isPurgePolicyPauseReason 判断暂停原因是否属于「权限失效」类。
func isPurgePolicyPauseReason(reason string) bool {
	switch reason {
	case purgePauseReasonKeyRevoked, purgePauseReasonKeyPermissionRevoked, purgePauseReasonKeyScopeRevoked:
		return true
	}
	return false
}

// purgePauseReasonMessage 把机器可读的暂停原因翻译为用户可读文案。
func purgePauseReasonMessage(reason string) string {
	switch reason {
	case purgePauseReasonKeyRevoked:
		return "创建该任务的 API 秘钥已被删除或停用"
	case purgePauseReasonKeyPermissionRevoked:
		return "创建该任务的 API 秘钥已失去 inbox.purge 权限"
	case purgePauseReasonKeyScopeRevoked:
		return "账号已不在创建该任务的 API 秘钥账号范围内"
	}
	return "创建该任务的 API 秘钥权限已失效"
}

// IsActive 判断任务是否仍在进行(占用账号锁)。
func (t PurgeTask) IsActive() bool {
	switch t.Status {
	case "running", "throttled", "paused":
		return true
	}
	return false
}

// ====================================================================
// 管理器
// ====================================================================

// purgeManager 管理清理任务: 状态机 + 按账号互斥 + 异步执行。
type purgeManager struct {
	mu     sync.Mutex
	tasks  map[string]*PurgeTask
	order  []string // 创建顺序(用于保留策略)
	file   string
	active map[string]string // accountID → 活跃任务 ID
	// running 标记任务 ID → 是否有 runner 正在跑(单飞守卫: 防同任务双 runner)。
	running map[string]bool
	// now 可注入(测试确定化)。
	now func() time.Time
	// sleep 可注入(测试跳过块间等待)。
	sleep func(time.Duration)
	// withClient 提供账号的 IMAP 客户端(生产 = account.Manager.WithMailClient)。
	// 为 nil 时执行器报错(测试须注入)。
	withClient func(accountID string, fn func(*mail.Client) error) error
	// invalidateReads 让账号的读缓存失效(收件箱/摘要); nil 时空操作。
	// 每块删除成功后调用——否则 30s 收件箱缓存仍展示已删邮件。
	invalidateReads func(accountID string)
	// keyGuard 在执行期重校任务创建者秘钥的当前权限(每块删除前调用)。
	// 返回 !ok 时 reason 为暂停原因(key_revoked/key_permission_revoked/
	// key_scope_revoked)。nil = 不校验(向后兼容: 既有测试/未接线部署)。
	keyGuard func(keyID, accountID string) (ok bool, reason string)
	// keyGuardExplicit 标记 keyGuard 已由 SetKeyGuard 显式注入(含 nil):
	// 此时 ensureKeyGuard 不再覆盖调用方的选择。
	keyGuardExplicit bool
	// logFn 写审计日志(可空)。
	logFn func(taskID, level, format string, args ...any)
}

// newPurgeManager 建管理器; file 为空 = 纯内存。
func newPurgeManager(file string, withClient func(string, func(*mail.Client) error) error) (*purgeManager, error) {
	m := &purgeManager{
		tasks:      map[string]*PurgeTask{},
		file:       file,
		active:     map[string]string{},
		running:    map[string]bool{},
		now:        time.Now,
		sleep:      time.Sleep,
		withClient: withClient,
	}
	if file != "" {
		if err := m.load(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// SetKeyGuard 注入执行期秘钥校验器(生产 = purgeKeyGuard(keys))。
//
// fn 在每个 chunk 删除前被调用: 返回 !ok 时任务立即 paused 并记录 reason,
// 不再删除任何邮件。传 nil 关闭校验(向后兼容)。
func (m *purgeManager) SetKeyGuard(fn func(keyID, accountID string) (ok bool, reason string)) {
	m.mu.Lock()
	m.keyGuard = fn
	m.keyGuardExplicit = true
	m.mu.Unlock()
}

// ensureKeyGuard 在尚未显式注入校验器时用秘钥管理器兜底接线。
//
// server.go 的组装代码由其它车道持有, 因此由清理 handler 在入口处保证
// 生产路径一定装上执行期校验; 已显式 SetKeyGuard(含 nil)则尊重调用方选择。
func (m *purgeManager) ensureKeyGuard(keys *apikey.Manager) {
	if keys == nil {
		return
	}
	m.mu.Lock()
	if !m.keyGuardExplicit {
		m.keyGuard = purgeKeyGuard(keys)
	}
	m.mu.Unlock()
}

// keyGuardFn 快照当前校验器(避免与 SetKeyGuard 竞态)。
func (m *purgeManager) keyGuardFn() func(keyID, accountID string) (ok bool, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keyGuard
}

// purgeKeyGuard 基于秘钥管理器构造执行期校验函数。
//
// 判定顺序(失败关闭): 秘钥不存在/已停用 → key_revoked; 缺 inbox.purge →
// key_permission_revoked; 账号不在秘钥范围内 → key_scope_revoked。
func purgeKeyGuard(keys *apikey.Manager) func(keyID, accountID string) (ok bool, reason string) {
	return func(keyID, accountID string) (bool, string) {
		if keys == nil {
			return true, ""
		}
		k, ok := keys.Get(keyID)
		if !ok || !k.Enabled {
			return false, purgePauseReasonKeyRevoked
		}
		if !k.HasPermission("inbox.purge") {
			return false, purgePauseReasonKeyPermissionRevoked
		}
		if !k.AllowsAccount(accountID) {
			return false, purgePauseReasonKeyScopeRevoked
		}
		return true, ""
	}
}

// checkKeyForTask 校验任务创建者秘钥当前是否仍有权对该账号执行清理。
//
// 会话创建的任务(CreatedByKeyID 为空)或未注入 keyGuard 时视为通过
// (向后兼容)。返回 !ok 时 reason 已归一化为约定的三种取值之一。
func (m *purgeManager) checkKeyForTask(tk PurgeTask) (bool, string) {
	if tk.CreatedByKeyID == "" {
		return true, ""
	}
	guard := m.keyGuardFn()
	if guard == nil {
		return true, ""
	}
	ok, reason := guard(tk.CreatedByKeyID, tk.AccountID)
	if ok {
		return true, ""
	}
	if reason == "" {
		reason = purgePauseReasonKeyRevoked
	}
	return false, reason
}

// authorizeChunk 是每个 chunk 删除前的权限检查点。
//
// 返回 false 表示任务已被暂停(或已消失), 调用方必须立即停止, 不得再删除
// 任何邮件。
func (m *purgeManager) authorizeChunk(taskID string) bool {
	tk, ok := m.get(taskID)
	if !ok {
		return false
	}
	ok, reason := m.checkKeyForTask(tk)
	if !ok {
		m.pauseWithPolicyReason(taskID, reason)
	}
	return ok
}

// pauseWithPolicyReason 因创建者秘钥权限失效而暂停任务: 记录机器可读原因
// 与用户可读文案, 落盘, 并写一条 warn 级任务日志(category purge 由
// server 侧 logFn 接线标注)。
//
// 这类暂停不可恢复(见 resume), 因此同时释放账号锁: 否则「请用新秘钥重建
// 任务」会因同账号互斥一直 409, 而失效秘钥又无权 cancel, 形成死锁。
func (m *purgeManager) pauseWithPolicyReason(taskID, reason string) {
	msg := purgePauseReasonMessage(reason)
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return
	}
	// 终态任务不得被权限检查「复活」为 paused(取消与块检查可能并发)。
	if t.Status == "done" || t.Status == "failed" || t.Status == "cancelled" {
		m.mu.Unlock()
		return
	}
	t.Status = "paused"
	t.PauseReason = reason
	t.Error = msg
	t.CurrentMessage = msg
	if m.active[t.AccountID] == t.ID {
		delete(m.active, t.AccountID)
	}
	_ = m.saveLocked()
	m.mu.Unlock()
	m.log(taskID, "warn", "清理任务已暂停: %s (pause_reason=%s)", msg, reason)
}

// load 读取持久化任务; 运行中的任务标记 interrupted(重启中断)。
func (m *purgeManager) load() error {
	raw, err := os.ReadFile(m.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var wrapper struct {
		Tasks []*PurgeTask `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return fmt.Errorf("解析清理任务文件: %w", err)
	}
	for _, t := range wrapper.Tasks {
		if t == nil || t.ID == "" {
			continue
		}
		// 运行中的任务在重启后无法继续执行(进程内的 runner 已消失):
		// 标记 interrupted, 用户可显式 resume。
		if t.Status == "running" || t.Status == "throttled" {
			t.Status = "interrupted"
		}
		m.tasks[t.ID] = t
		m.order = append(m.order, t.ID)
	}
	return nil
}

// saveLocked 原子写盘(调用方持锁)。纯内存模式为空操作。
func (m *purgeManager) saveLocked() error {
	if m.file == "" {
		return nil
	}
	tasks := make([]*PurgeTask, 0, len(m.order))
	for _, id := range m.order {
		if t, ok := m.tasks[id]; ok {
			tasks = append(tasks, t)
		}
	}
	return writeJSONAtomic(m.file, map[string]any{"tasks": tasks})
}

// create 创建任务(会话创建: 无创建者秘钥)。同账号已有活跃任务时返回错误。
func (m *purgeManager) create(accountID, accountName string, start, end time.Time) (PurgeTask, error) {
	return m.createWithKey(accountID, accountName, start, end, "")
}

// createWithKey 创建任务并记录创建者秘钥 ID(空 = 管理员会话创建)。
func (m *purgeManager) createWithKey(accountID, accountName string, start, end time.Time, createdByKeyID string) (PurgeTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.active[accountID]; busy {
		return PurgeTask{}, fmt.Errorf("该账号已有清理任务在运行")
	}
	// 清理任务不重复: 已存在活跃任务的账号直接拒绝(即使 active 表因重启为空,
	// 也检查任务表——双保险)。权限失效暂停的任务永不恢复且已释放账号锁,
	// 不算「活跃」——否则吊销秘钥后无法用新秘钥重建任务。
	for _, id := range m.order {
		t := m.tasks[id]
		if t.AccountID == accountID && t.IsActive() && !isPurgePolicyPauseReason(t.PauseReason) {
			return PurgeTask{}, fmt.Errorf("该账号已有清理任务在运行")
		}
	}
	tk := &PurgeTask{
		ID:             "purge_" + randomHexToken(8),
		AccountID:      accountID,
		AccountName:    accountName,
		CreatedByKeyID: createdByKeyID,
		RangeStart:     start.Format(time.RFC3339),
		RangeEnd:       end.Format(time.RFC3339),
		Status:         "running",
		StartedAt:      m.now().Format(time.RFC3339),
	}
	m.tasks[tk.ID] = tk
	m.order = append(m.order, tk.ID)
	m.active[accountID] = tk.ID
	m.trimLocked()
	if err := m.saveLocked(); err != nil {
		delete(m.tasks, tk.ID)
		m.order = m.order[:len(m.order)-1]
		delete(m.active, accountID)
		return PurgeTask{}, err
	}
	return *tk, nil
}

// trimLocked 保留策略: 活跃任务无条件保留且不计入上限; 仅对终态任务按
// 新→旧保留 purgeKeepTasks 条。order 保持旧→新顺序。
func (m *purgeManager) trimLocked() {
	if len(m.order) <= purgeKeepTasks {
		return
	}
	keep := make([]string, 0, purgeKeepTasks+len(m.order))
	// 先无条件收活跃任务(不占额度)。
	for _, id := range m.order {
		if t, ok := m.tasks[id]; ok && t.IsActive() {
			keep = append(keep, id)
		}
	}
	// 终态任务从新到旧补足额度。
	finished := 0
	for i := len(m.order) - 1; i >= 0; i-- {
		if finished >= purgeKeepTasks {
			break
		}
		id := m.order[i]
		t, ok := m.tasks[id]
		if !ok || t.IsActive() {
			continue
		}
		keep = append(keep, id)
		finished++
	}
	// 恢复为旧→新顺序(与 order 语义一致)。
	pos := make(map[string]int, len(m.order))
	for i, id := range m.order {
		pos[id] = i
	}
	sort.Slice(keep, func(i, j int) bool { return pos[keep[i]] < pos[keep[j]] })
	kept := map[string]bool{}
	for _, id := range keep {
		kept[id] = true
	}
	for id := range m.tasks {
		if !kept[id] {
			delete(m.tasks, id)
		}
	}
	m.order = keep
}

// get 取单个任务。
func (m *purgeManager) get(id string) (PurgeTask, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return PurgeTask{}, false
	}
	return *t, true
}

// list 返回全部任务(新→旧)。
func (m *purgeManager) list() []PurgeTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PurgeTask, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		if t, ok := m.tasks[m.order[i]]; ok {
			out = append(out, *t)
		}
	}
	return out
}

// update 应用变更(持锁快照 + 写盘)。
func (m *purgeManager) update(id string, fn func(*PurgeTask)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	fn(t)
	return m.saveLocked()
}

// finish 结束任务并释放账号锁(仅释放属于自己的锁, 防误放他人)。
func (m *purgeManager) finish(id, status, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return
	}
	t.Status = status
	if errMsg != "" {
		t.Error = errMsg
	}
	if status == "done" || status == "failed" || status == "cancelled" {
		t.FinishedAt = m.now().Format(time.RFC3339)
		// 双 runner 场景: 先完成者不得放掉后来者的账号锁。
		if m.active[t.AccountID] == t.ID {
			delete(m.active, t.AccountID)
		}
		// 任务转入终态后应用保留策略(活跃任务仍无条件保留)。
		m.trimLocked()
	}
	_ = m.saveLocked()
}

// pause 暂停任务(保留账号锁, 可 resume)。
func (m *purgeManager) pause(id string) error {
	return m.update(id, func(t *PurgeTask) {
		if t.Status == "running" || t.Status == "throttled" {
			t.Status = "paused"
		}
	})
}

// resume 继续暂停/中断的任务。
//
// 权限失效暂停(PauseReason ∈ key_revoked/key_permission_revoked/
// key_scope_revoked)的任务一律拒绝 resume(会话亦不例外): 秘钥已被删除/
// 停用/降权, 继续执行等于绕过吊销, 必须用有效秘钥重建任务。
//
// 同账号互斥: 若账号已有其它活跃任务(active 映射指向别的任务 ID),
// 拒绝 resume——否则同账号会并发两个 runner, IMAP 命令数翻倍。
func (m *purgeManager) resume(id string) error {
	tk, ok := m.get(id)
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if isPurgePolicyPauseReason(tk.PauseReason) {
		return ErrResumeDeniedByPolicy
	}
	// 只有 paused/interrupted 可恢复: 先判状态, 避免对 done/cancelled 的
	// 任务做权限检查时把它改写成 paused。
	switch tk.Status {
	case "paused", "interrupted":
	default:
		return fmt.Errorf("任务状态 %s 不可继续", tk.Status)
	}
	// 暂停后秘钥可能已失效(如重启遗留的 interrupted 任务): 先重校再恢复,
	// 避免 resume 后 runner 才暂停(既产生一次空跑, 也让状态在两次请求间抖动)。
	if ok, reason := m.checkKeyForTask(tk); !ok {
		m.pauseWithPolicyReason(id, reason)
		return ErrResumeDeniedByPolicy
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if cur, busy := m.active[t.AccountID]; busy && cur != id {
		return fmt.Errorf("该账号已有清理任务在运行")
	}
	switch t.Status {
	case "paused", "interrupted":
		t.Status = "running"
		t.PauseReason = "" // 普通暂停恢复: 清掉暂停原因
		m.active[t.AccountID] = id
	default:
		return fmt.Errorf("任务状态 %s 不可继续", t.Status)
	}
	return m.saveLocked()
}

// cancel 取消任务并释放账号锁。
func (m *purgeManager) cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if t.Status == "done" || t.Status == "failed" || t.Status == "cancelled" {
		return nil
	}
	t.Status = "cancelled"
	t.FinishedAt = m.now().Format(time.RFC3339)
	// 只释放自己持有的账号锁: 权限失效暂停会释放锁并把账号让给新任务,
	// 此时取消旧任务不得误删新任务的锁。
	if m.active[t.AccountID] == t.ID {
		delete(m.active, t.AccountID)
	}
	return m.saveLocked()
}

// setTotalFromPreview 用一次 SEARCH 统计待删总数, 写入 Total(进度条分母)。
// 失败/超时都不阻塞任务(Total 保持 0, 前端显示不确定进度)。
func (m *purgeManager) setTotalFromPreview(taskID string, start, end time.Time) {
	if m.withClient == nil {
		return
	}
	tk, ok := m.get(taskID)
	if !ok {
		return
	}
	total := 0
	err := withIMAPTimeout(purgeOpTimeout, func() error {
		return m.withClient(tk.AccountID, func(c *mail.Client) error {
			for _, folder := range purgeFolders {
				uids, missing, e := c.SearchRange(folder, mail.DateRange{Start: start, End: end})
				if e != nil {
					return e
				}
				if missing {
					continue
				}
				total += len(uids)
			}
			return nil
		})
	})
	if err != nil {
		return
	}
	_ = m.update(taskID, func(t *PurgeTask) { t.Total = total })
}

// randomHexToken 生成 n 字节的随机 hex(任务 ID 用)。
func randomHexToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())[:n*2]
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}
