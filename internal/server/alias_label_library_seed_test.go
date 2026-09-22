package server

import "testing"

// 单任务：在抽满整个名称库之前不应出现重复。
func TestLibrarySeedNoRepeatWithinRound(t *testing.T) {
	const seed uint64 = 12345
	n := len(aliasLabelLibrary)
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		label := libraryLabelForSeed(seed, i)
		if seen[label] {
			t.Fatalf("label %q repeated within a single round at ordinal %d", label, i)
		}
		seen[label] = true
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct labels in one round, got %d", n, len(seen))
	}
}

// 确定性：同一 seed + ordinal 必须稳定复现（重启后可无缝续上）。
func TestLibrarySeedDeterministic(t *testing.T) {
	const seed uint64 = 999
	for _, ord := range []int{0, 1, 42, 500, 1001} {
		a := libraryLabelForSeed(seed, ord)
		b := libraryLabelForSeed(seed, ord)
		if a != b {
			t.Fatalf("ordinal %d not deterministic: %q vs %q", ord, a, b)
		}
	}
}

// 跨任务错开：不同 seed 的前 K 个抽取应显著不同（不是逐位相同的顺序序列）。
func TestLibrarySeedDivergesAcrossTasks(t *testing.T) {
	const k = 30
	same := 0
	for i := 0; i < k; i++ {
		if libraryLabelForSeed(1, i) == libraryLabelForSeed(2, i) {
			same++
		}
	}
	// 两个独立排列在同一位置相同的期望约 k/N ≈ 0，允许极少量偶然重合。
	if same > 3 {
		t.Fatalf("seeds 1 and 2 overlapped %d/%d positions — sequences not diverging", same, k)
	}
}

// 下一轮重新洗牌：第二轮不应与第一轮同序。
func TestLibrarySeedReshufflesNextRound(t *testing.T) {
	const seed uint64 = 7
	n := len(aliasLabelLibrary)
	same := 0
	for i := 0; i < n; i++ {
		if libraryLabelForSeed(seed, i) == libraryLabelForSeed(seed, i+n) {
			same++
		}
	}
	if same == n {
		t.Fatal("round 2 is identical to round 1 — reshuffle not applied")
	}
}

// 旧任务兼容：LabelSeed 为 0 时按 ID 派生稳定非零种子，且不同 ID 产生不同序列。
func TestLibrarySeedFallbackByID(t *testing.T) {
	a := AliasTask{ID: "task_aaaa"}
	b := AliasTask{ID: "task_bbbb"}
	if a.librarySeed() == 0 || b.librarySeed() == 0 {
		t.Fatal("derived seed must be non-zero")
	}
	if a.librarySeed() == b.librarySeed() {
		t.Fatal("different task IDs should derive different seeds")
	}
	// 显式 LabelSeed 优先于 ID 派生。
	c := AliasTask{ID: "task_aaaa", LabelSeed: 42}
	if c.librarySeed() != 42 {
		t.Fatalf("explicit LabelSeed should win, got %d", c.librarySeed())
	}
}

// newLabelSeed 必须返回非零种子。
func TestNewLabelSeedNonZero(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if newLabelSeed() == 0 {
			t.Fatal("newLabelSeed returned 0")
		}
	}
}
