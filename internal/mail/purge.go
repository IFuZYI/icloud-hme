package mail

import (
	"fmt"

	"github.com/emersion/go-imap"
)

// DefaultPurgeChunkSize 是批量删除的默认块大小(邮件数/块)。
//
// 每块 = 1×STORE + 1×EXPUNGE(2 次往返)。500 封/块把 1 万封的成本压到
// ~40 次往返; 块越小越"温和"但命令越多——500 是风控与效率的平衡点。
const DefaultPurgeChunkSize = 500

// ====================================================================
// 批量清理删除: 按时间区间分块删除邮件
// ====================================================================
//
// 风控设计(核心):
//   - 逐封删除 = 每封 1×STORE + 1×EXPUNGE(2N 次往返), N 大时是风控灾难;
//   - 分块删除 = 每块 1×STORE(seqset 覆盖整块) + 1×EXPUNGE,
//     12 封/块 5 → 3 次 STORE + 3 次 EXPUNGE, 命令数降一个数量级;
//   - 连续 UID 由 imap.SeqSet 自动合并为区间(协议层压缩, "2000:12000");
//   - 块内原子: 一块内所有邮件要么全标记全清, 要么不动作(STORE 失败即中止)。
//
// 语义边界:
//   - EXPUNGE 是文件夹级: 会清掉该文件夹内所有 \Deleted 标记的邮件
//     (go-imap v1 无 UID EXPUNGE/UIDPLUS)。这是标准 IMAP 行为, 与单封删除
//     路径(DeleteFrom)一致——若其它客户端有「延迟 expunge」的待删邮件,
//     会被一并清掉。批量清理语义下可接受(用户主动清理, 且各主流服务端
//     在批量操作后本就执行 expunge)。
//   - 只处理 SEARCH 命中的 UID(按日期条件), 不触碰区间外邮件。

// PurgeResult 是一次 PurgeRange 的结果统计。
type PurgeResult struct {
	// Deleted 是实际删除(标记+expunge)的邮件数。
	Deleted int
	// Scanned 是 SEARCH 命中的候选数(= Deleted, 除非中途失败)。
	Scanned int
	// Chunks 是执行的块数(命令预算的可观测指标)。
	Chunks int
	// MissingFolder 为 true 表示文件夹不存在(视为 0 封, 非错误)。
	MissingFolder bool
}

// SearchRange 在 folder 中按区间搜索 UID(升序)。
//
// 返回 (uids, missing, err): missing=true 表示文件夹不存在(视为 0 封)。
// 供清理任务在块间保持控制(先取全量 UID, 再本地分块节流删除)。
func (c *Client) SearchRange(folder string, r DateRange) ([]uint32, bool, error) {
	if c.cli == nil {
		return nil, false, fmt.Errorf("未连接")
	}
	if folder == "" {
		folder = "INBOX"
	}
	if _, err := c.selectFolder(folder, true); err != nil {
		if isNoSuchFolder(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	uids, err := c.cli.UidSearch(dateRangeSearchCriteria(r))
	if err != nil {
		return nil, false, err
	}
	sortUIDsAsc(uids)
	return uids, false, nil
}

// DeleteUIDChunk 删除 folder 中指定 UID 集合的邮件(一块)。
//
// 命令预算: 1×SELECT + 1×UID STORE(整块 seqset) + 1×EXPUNGE = 3 次往返。
// 连续 UID 由 SeqSet 自动合并为区间。
func (c *Client) DeleteUIDChunk(folder string, uids []uint32) error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	if len(uids) == 0 {
		return nil
	}
	if folder == "" {
		folder = "INBOX"
	}
	if _, err := c.selectFolder(folder, false); err != nil {
		return err
	}
	seqset := new(imap.SeqSet)
	seqset.AddNum(uids...)
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := c.cli.UidStore(seqset, item, []interface{}{imap.DeletedFlag}, nil); err != nil {
		return err
	}
	if err := c.cli.Expunge(nil); err != nil {
		return err
	}
	c.InvalidateListSnapshots()
	return nil
}

// PurgeRange 删除 folder 中落在区间 r 内的所有邮件, 按 chunkSize 分块。
//
// chunkSize <= 0 时使用 DefaultPurgeChunkSize。返回各计数供任务进度统计。
// 文件夹不存在时返回 MissingFolder=true 且不报错(iCloud 无「垃圾邮件」、
// 163 无 Junk——与读信路径同口径)。
//
// 调用方负责块间节流(purge 管理器按 purgeChunkDelay 控制节奏); 本函数
// 单次调用内连续执行全部块, 不 sleep——便于测试注入与上层调度。
func (c *Client) PurgeRange(folder string, r DateRange, chunkSize int) (PurgeResult, error) {
	var res PurgeResult
	if chunkSize <= 0 {
		chunkSize = DefaultPurgeChunkSize
	}
	uids, missing, err := c.SearchRange(folder, r)
	if err != nil {
		return res, err
	}
	if missing {
		return PurgeResult{MissingFolder: true}, nil
	}
	if len(uids) == 0 {
		return res, nil
	}
	res.Scanned = len(uids)

	// 分块: 每块一次 STORE(整块 seqset) + 一次 EXPUNGE。
	for start := 0; start < len(uids); start += chunkSize {
		end := start + chunkSize
		if end > len(uids) {
			end = len(uids)
		}
		if err := c.DeleteUIDChunk(folder, uids[start:end]); err != nil {
			// 块内失败: 已标记的邮件保留(下次 EXPUNGE 或用户重试时清理);
			// 返回已完成的计数, 由调用方决定重试策略。
			res.Chunks++
			return res, err
		}
		res.Deleted += end - start
		res.Chunks++
	}
	return res, nil
}
