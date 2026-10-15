package mail

import (
	"testing"
	"time"
)

// ====================================================================
// Web API 回退路径的日期过滤
// ====================================================================
//
// 回归背景: IMAP 路径按日期区间在服务端 SEARCH 过滤; Web API 回退路径
// 无日期过滤——用户选「最近 1 天」走回退时会看到超出区间的邮件, 与 IMAP
// 路径语义不一致(审查发现)。
//
// 契约: FilterByDateRange 过滤区间外邮件; 时间缺失(零值)一律保留,
// 与 IMAP 路径 within 的「宁可多显示也不误丢」原则一致。
func TestFilterByDateRange(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.Local)
	mk := func(id string, when time.Time) Message {
		m := Message{ID: id, Subject: "邮件" + id}
		if !when.IsZero() {
			m.Date = when.Format(time.RFC3339)
		}
		return m
	}
	older := mk("1", base.Add(-48*time.Hour))
	inRange := mk("2", base.Add(-time.Hour))
	newer := mk("3", base.Add(24*time.Hour))
	noDate := mk("4", time.Time{})

	cases := []struct {
		name string
		list []Message
		r    DateRange
		want []string
	}{
		{
			"零值区间全部保留",
			[]Message{older, inRange, newer, noDate},
			DateRange{},
			[]string{"1", "2", "3", "4"},
		},
		{
			"下界排除更早邮件",
			[]Message{older, inRange, newer, noDate},
			DateRange{Start: base.Add(-24 * time.Hour)},
			[]string{"2", "3", "4"},
		},
		{
			"上界排除更新邮件",
			[]Message{older, inRange, newer, noDate},
			DateRange{End: base},
			[]string{"1", "2", "4"},
		},
		{
			"双界区间",
			[]Message{older, inRange, newer, noDate},
			DateRange{Start: base.Add(-24 * time.Hour), End: base.Add(time.Hour)},
			[]string{"2", "4"},
		},
		{
			"全部被排除时返回空切片而非 nil",
			[]Message{older, newer},
			DateRange{Start: base.Add(-time.Hour), End: base.Add(time.Hour)},
			[]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterByDateRange(tc.list, tc.r)
			if got == nil {
				t.Fatal("结果应为非 nil 切片(JSON 契约: messages 不能为 null)")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("过滤后 %d 封, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, id := range tc.want {
				if got[i].ID != id {
					t.Fatalf("第 %d 封 = %q, want %q", i, got[i].ID, id)
				}
			}
		})
	}
}
