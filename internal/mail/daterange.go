package mail

import (
	"time"

	"github.com/emersion/go-imap"
)

// DateRange 是按收件时间(INTERNALDATE)过滤的闭区间 [Start, End]。
//
// 零值表示不限: Start 为零则不设下界, End 为零则不设上界。
// 相比旧的「近 N 天」days 参数,区间同时支持两端,
// 使前端可以直接用日期选择器表达「任意起止」。
type DateRange struct {
	Start time.Time
	End   time.Time
}

// isZero 表示区间两端都未设置(即不限时间)。
func (r DateRange) isZero() bool { return r.Start.IsZero() && r.End.IsZero() }

// within 判断收件时间是否落在区间内。时间缺失(零值)一律保留,
// 与旧 days 过滤的「宁可多显示也不误丢」原则一致。
func (r DateRange) within(when time.Time) bool {
	if when.IsZero() {
		return true
	}
	if !r.Start.IsZero() && when.Before(r.Start) {
		return false
	}
	if !r.End.IsZero() && when.After(r.End) {
		return false
	}
	return true
}

// DateRangeFromDays 把旧的「近 N 天」参数转换为区间: [now-N天, 不设上界)。
// days<=0 返回零值(不限)。
func DateRangeFromDays(days int) DateRange {
	if days <= 0 {
		return DateRange{}
	}
	return DateRange{Start: time.Now().AddDate(0, 0, -days)}
}

// dateRangeSearchCriteria 把区间翻译为 IMAP SEARCH 条件。
//
// 关键语义: RFC 3501 的日期条件按「日粒度、忽略时间与时区」比较, 且 BEFORE
// 是排他的。因此 BEFORE 必须取「End 所在日的次日零点」——直接传 End
// (哪怕是 23:59:59)会让最后一天整天的邮件被排除, 单日区间直接返回空。
func dateRangeSearchCriteria(r DateRange) *imap.SearchCriteria {
	criteria := imap.NewSearchCriteria()
	if !r.Start.IsZero() {
		criteria.Since = r.Start
	}
	if !r.End.IsZero() {
		// End 当天要含在内: BEFORE <次日零点> 恰好覆盖 [.., End 当天]。
		y, m, d := r.End.Date()
		criteria.Before = time.Date(y, m, d, 0, 0, 0, 0, r.End.Location()).AddDate(0, 0, 1)
	}
	return criteria
}
