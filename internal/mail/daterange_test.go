package mail

import (
	"testing"
	"time"
)

// 日期区间过滤: 有 end 时不能只看 since,必须同时排除 end 之后的邮件。
func TestDateRangeWithin(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.Local)
	cases := []struct {
		name string
		when time.Time
		r    DateRange
		want bool
	}{
		{"零值区间全部通过", base, DateRange{}, true},
		{"早于 start 被排除", base.Add(-48 * time.Hour), DateRange{Start: base.Add(-24 * time.Hour)}, false},
		{"晚于 start 通过", base, DateRange{Start: base.Add(-24 * time.Hour)}, true},
		{"晚于 end 被排除", base.Add(24 * time.Hour), DateRange{End: base}, false},
		{"等于 end 通过", base, DateRange{End: base}, true},
		{"区间内通过", base, DateRange{Start: base.Add(-time.Hour), End: base.Add(time.Hour)}, true},
		{"时间缺失一律保留", time.Time{}, DateRange{Start: base, End: base}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.within(tc.when); got != tc.want {
				t.Fatalf("within(%v) = %v, want %v", tc.when, got, tc.want)
			}
		})
	}
}

// days 是旧参数,应等价于 [now-days, 无穷)。
func TestDateRangeFromDays(t *testing.T) {
	now := time.Now()
	r := DateRangeFromDays(7)
	if r.End.IsZero() == false {
		t.Fatal("days 转换不应设置 End")
	}
	delta := now.AddDate(0, 0, -7).Sub(r.Start)
	if delta > time.Second || delta < -time.Second {
		t.Fatalf("Start = %v, 期望约 7 天前", r.Start)
	}
	if !DateRangeFromDays(0).isZero() {
		t.Fatal("days=0 应表示不限")
	}
}

// IMAP 条件: 区间两端都要进 SEARCH(Since + Before)。
// Before 必须取 End 的次日零点——RFC 3501 的日期比较是日粒度且排他,
// 直接传 End 会把 End 当天的邮件全部排除(单日区间返回空)。
func TestDateRangeSearchCriteria(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)
	end := time.Date(2026, 8, 10, 0, 0, 0, 0, time.Local)
	criteria := dateRangeSearchCriteria(DateRange{Start: start, End: end})
	if criteria.Since.IsZero() || !criteria.Since.Equal(start) {
		t.Fatalf("Since = %v, want %v", criteria.Since, start)
	}
	wantBefore := time.Date(2026, 8, 11, 0, 0, 0, 0, time.Local)
	if criteria.Before.IsZero() || !criteria.Before.Equal(wantBefore) {
		t.Fatalf("Before = %v, want %v(End 的次日零点)", criteria.Before, wantBefore)
	}
	// 零值区间不添加任何条件。
	empty := dateRangeSearchCriteria(DateRange{})
	if !empty.Since.IsZero() || !empty.Before.IsZero() {
		t.Fatal("零值区间不应产生 SEARCH 条件")
	}
	// 带时间的 End(如 23:59:59)同样归一到次日零点, 保证含整天。
	endLate := time.Date(2026, 8, 10, 23, 59, 59, 0, time.Local)
	criteria2 := dateRangeSearchCriteria(DateRange{End: endLate})
	if !criteria2.Before.Equal(wantBefore) {
		t.Fatalf("带时间 End 的 Before = %v, want %v", criteria2.Before, wantBefore)
	}
}
