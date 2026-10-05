package mail

import (
	"testing"
	"time"
)

// 端到端: End=今天的区间必须包含今天的邮件。
//
// 回归背景: IMAP BEFORE 是日粒度排他比较, 此前直接把 End(当天 0 点)传给
// Before, 导致 End 当天的邮件被整日排除(单日区间返回空)。修复后
// Before = End 的次日零点, End 当天被包含。
func TestDateRangeEndIncludesEndDayE2E(t *testing.T) {
	c, be := newE2EServerWithBackend(t)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	addMessageTo(t, be, "INBOX", "a@example.com", "alias@icloud.com", "今天的邮件", now.Add(-2*time.Hour))
	addMessageTo(t, be, "INBOX", "b@example.com", "alias@icloud.com", "两天前的邮件", now.AddDate(0, 0, -2))

	// 区间 [两天前, 今天]: 今天的邮件必须在内(未修复时会被整日排除)。
	msgs, total, err := c.ListInboxPageRange(10, 0, DateRange{Start: today.AddDate(0, 0, -2), End: today})
	if err != nil {
		t.Fatalf("ListInboxPageRange: %v", err)
	}
	if !containsSubject(msgs, "今天的邮件") {
		t.Fatalf("End=今天的区间必须包含今天的邮件, got total=%d %+v", total, msgs)
	}

	// 别名路径同样必须包含今天的邮件。
	msgs2, _, err := c.FindByRecipientRange("alias@icloud.com", 10, 0, DateRange{Start: today.AddDate(0, 0, -2), End: today})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubject(msgs2, "今天的邮件") {
		t.Fatalf("别名路径 End=今天必须包含今天的邮件, got %+v", msgs2)
	}

	// 区间 [三天前, 昨天]: 今天的邮件必须被排除, 昨天的邮件保留。
	addMessageTo(t, be, "INBOX", "c@example.com", "alias@icloud.com", "昨天的邮件", now.AddDate(0, 0, -1))
	msgs3, _, err := c.ListInboxPageRange(10, 0, DateRange{Start: today.AddDate(0, 0, -3), End: today.AddDate(0, 0, -1)})
	if err != nil {
		t.Fatal(err)
	}
	if containsSubject(msgs3, "今天的邮件") {
		t.Fatalf("End=昨天时今天的邮件应被排除, got %+v", msgs3)
	}
	if !containsSubject(msgs3, "昨天的邮件") {
		t.Fatalf("End=昨天时昨天的邮件应保留, got %+v", msgs3)
	}
}

func containsSubject(msgs []Message, subject string) bool {
	for _, m := range msgs {
		if m.Subject == subject {
			return true
		}
	}
	return false
}
