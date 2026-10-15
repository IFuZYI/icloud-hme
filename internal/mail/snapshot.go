// IMAP 列表 UID 快照与 UIDVALIDITY 跟踪。
//
// 翻页(load more)、切筛选再切回、同一页的重复查询都会重复「全量 UID SEARCH」;
// 网易 Coremail 上「按别名筛选」还会退化为扫最近信封(更贵)。对外部收件邮箱
// (163 等), 高频扫箱是风控/封禁的典型触发信号(见 internal/server/cache.go
// 的同类说明)。本文件提供短 TTL 快照, 让同一逻辑查询只回源一次。
//
// UIDVALIDITY 跟踪: 邮箱被服务端重建(如 163 后台修复文件夹)后, 旧 UID 不再
// 指向同一邮件; 继续使用快照/缓存会展示或操作错误的邮件。检测到变化时
// 立即作废本客户端的全部快照, 并暴露一次性标记供上层失效其读缓存。
package mail

import (
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap"
)

// ListSnapshotTTL 是 IMAP 列表快照(UID 集合 / 回退扫描结果)的有效期;
// 0 表示关闭快照(测试用)。
//
// 30s 与 server 层响应缓存同一量级: 覆盖「翻页/切筛选」的连续操作窗口,
// 又不至于让新邮件长时间不可见。
var ListSnapshotTTL = 30 * time.Second

// listSnapshot 是某 (文件夹, 收件人, 日期区间) 的 SEARCH 结果快照。
type listSnapshot struct {
	uids []uint32
	// boxHasMessages 记录采集时的文件夹非空状态, 供别名路径的
	// 「SEARCH 静默失效」判定在快照命中时保持同样的兜底行为。
	boxHasMessages bool
	expiresAt      time.Time
}

// recentCandidate 是回退扫描(扫最近信封本地过滤)命中的候选邮件。
type recentCandidate struct {
	folder string
	uid    uint32
	date   time.Time
	to     string
}

// recentScanSnapshot 是回退扫描结果的快照(按新→旧排好序)。
type recentScanSnapshot struct {
	cands      []recentCandidate
	scanBudget int
	// coveredAll 表示采集时各文件夹的最近 scan 封已覆盖全部邮件
	// (scan == 文件夹总数)——此时快照是完整的, 可服务任意更大的请求预算。
	coveredAll bool
	expiresAt  time.Time
}

// listScope 生成快照键: 文件夹 + 收件人(别名路径) + 日期区间。
//
// 日期区间量化到分钟: days 回退路径由上层用 time.Now() 生成区间, 纳秒级
// 漂移会让同一逻辑查询每次生成新键, 快照永不命中(与 server 层缓存键的
// 量化理由相同)。
func listScope(folder, recipient string, r DateRange) string {
	var start, end int64
	if !r.Start.IsZero() {
		start = r.Start.Truncate(time.Minute).Unix()
	}
	if !r.End.IsZero() {
		end = r.End.Truncate(time.Minute).Unix()
	}
	return fmt.Sprintf("%s|%s|%d|%d", folder, strings.ToLower(recipient), start, end)
}

// snapshotGet 读取未过期的列表快照; 过期项顺手清除。
func (c *Client) snapshotGet(key string) (listSnapshot, bool) {
	if ListSnapshotTTL <= 0 {
		return listSnapshot{}, false
	}
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	entry, ok := c.listSnapshots[key]
	if !ok {
		return listSnapshot{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.listSnapshots, key)
		return listSnapshot{}, false
	}
	return entry, true
}

// snapshotPut 写入列表快照。TTL<=0 时为空操作。
func (c *Client) snapshotPut(key string, snap listSnapshot) {
	if ListSnapshotTTL <= 0 {
		return
	}
	snap.expiresAt = time.Now().Add(ListSnapshotTTL)
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	if c.listSnapshots == nil {
		c.listSnapshots = make(map[string]listSnapshot)
	}
	c.listSnapshots[key] = snap
}

// recentSnapshotGet 读取未过期的回退扫描快照。
func (c *Client) recentSnapshotGet(key string) (recentScanSnapshot, bool) {
	if ListSnapshotTTL <= 0 {
		return recentScanSnapshot{}, false
	}
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	entry, ok := c.recentScans[key]
	if !ok {
		return recentScanSnapshot{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.recentScans, key)
		return recentScanSnapshot{}, false
	}
	return entry, true
}

// recentSnapshotPut 写入回退扫描快照。TTL<=0 时为空操作。
func (c *Client) recentSnapshotPut(key string, cands []recentCandidate, scanBudget int, coveredAll bool) {
	if ListSnapshotTTL <= 0 {
		return
	}
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	if c.recentScans == nil {
		c.recentScans = make(map[string]recentScanSnapshot)
	}
	c.recentScans[key] = recentScanSnapshot{
		cands:      cands,
		scanBudget: scanBudget,
		coveredAll: coveredAll,
		expiresAt:  time.Now().Add(ListSnapshotTTL),
	}
}

// InvalidateListSnapshots 作废全部快照(列表 UID 集合 + 回退扫描结果)。
//
// 调用时机: 删除邮件后(已删 UID 不得因快照残留继续出现)、显式刷新、
// UIDVALIDITY 变化(旧 UID 不再有效)。
func (c *Client) InvalidateListSnapshots() {
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	c.clearSnapshotsLocked()
}

// clearSnapshotsLocked 清空快照。调用方必须持有 c.snapMu。
func (c *Client) clearSnapshotsLocked() {
	c.listSnapshots = make(map[string]listSnapshot)
	c.recentScans = make(map[string]recentScanSnapshot)
}

// noteUidValidity 记录某文件夹观测到的 UIDVALIDITY。
//
// 变化时(RFC 3501 §2.3.1.1): 旧 UID 全部作废 → 清空快照并置一次性标记,
// 供上层失效服务端读缓存。首次观测只记录, 不产生变化标记(正常首连不误报)。
// v==0(服务器未报告)不参与检测。
func (c *Client) noteUidValidity(folder string, v uint32) {
	if v == 0 {
		return
	}
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	if c.uidValidity == nil {
		c.uidValidity = make(map[string]uint32)
	}
	if prev, ok := c.uidValidity[folder]; ok && prev != v {
		c.clearSnapshotsLocked()
		c.validityChanged = true
	}
	c.uidValidity[folder] = v
}

// TakeUidValidityChange 读取并清除「UIDVALIDITY 已变化」标记(一次性)。
// 上层(server.Backend)在 IMAP 读取后调用: 为 true 时失效该账号读缓存。
func (c *Client) TakeUidValidityChange() bool {
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	changed := c.validityChanged
	c.validityChanged = false
	return changed
}

// selectFolder SELECT 指定文件夹并记录其 UIDVALIDITY(邮箱重建检测)。
// 所有内部 SELECT 都应经此入口, 保证检测覆盖完整。
func (c *Client) selectFolder(folder string, readOnly bool) (*imap.MailboxStatus, error) {
	mbox, err := c.cli.Select(folder, readOnly)
	if err != nil {
		return nil, err
	}
	c.noteUidValidity(folder, mbox.UidValidity)
	return mbox, nil
}
