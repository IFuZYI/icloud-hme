package server

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// 收件箱接受日期区间参数 start/end(ISO 日期或 RFC3339),并解析为后端查询。
func TestInboxAcceptsDateRange(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	q := url.Values{}
	q.Set("account_id", "acc_1")
	q.Set("start", "2026-08-01")
	q.Set("end", "2026-08-10")
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/inbox?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("区间查询 = %d: %s", status, body)
	}
	if f.listInboxQuery.DateRange.Start.IsZero() || f.listInboxQuery.DateRange.End.IsZero() {
		t.Fatalf("日期区间未传入后端: %+v", f.listInboxQuery.DateRange)
	}
	if got := f.listInboxQuery.DateRange.Start.Format("2006-01-02"); got != "2026-08-01" {
		t.Fatalf("Start = %s", got)
	}
}

// end 应覆盖到当天最后一刻(含当天)。
func TestInboxDateRangeEndIncludesWholeDay(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/inbox?account_id=acc_1&start=2026-08-01&end=2026-08-10", nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	end := f.listInboxQuery.DateRange.End
	want := time.Date(2026, 8, 10, 23, 59, 59, 0, end.Location())
	if !end.Equal(want) {
		t.Fatalf("End = %v, want %v(含当天)", end, want)
	}
}

// 无 start/end 时保持旧行为: days=7 → 近 7 天。
func TestInboxFallsBackToDays(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/inbox?account_id=acc_1&days=7", nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if f.listInboxQuery.DateRange.Start.IsZero() {
		t.Fatal("days=7 应转换为 Start")
	}
	if !f.listInboxQuery.DateRange.End.IsZero() {
		t.Fatal("days 转换不应设置 End")
	}
}

// 非法日期应 400。
func TestInboxRejectsInvalidDate(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/inbox?account_id=acc_1&start=not-a-date", nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusBadRequest {
		t.Fatalf("非法日期应 400, got %d: %s", status, body)
	}
}

// 带时间的 start/end 原样保留(不再扩展为整天)——前端两个 DatePicker
// 各自带时间面板, 选择 08:30 就该是 08:30, 而不是被对齐成整天。
func TestInboxAcceptsDateTimeRange(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/inbox?account_id=acc_1&start=2026-08-01T08:30:00&end=2026-08-10T18:45:00", nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := f.listInboxQuery.DateRange
	wantStart := time.Date(2026, 8, 1, 8, 30, 0, 0, time.Local)
	wantEnd := time.Date(2026, 8, 10, 18, 45, 0, 0, time.Local)
	if !got.Start.Equal(wantStart) {
		t.Fatalf("Start = %v, want %v", got.Start, wantStart)
	}
	if !got.End.Equal(wantEnd) {
		t.Fatalf("End = %v, want %v(带时间不应扩展为整天)", got.End, wantEnd)
	}
}

// 不带秒的 datetime-local 格式(YYYY-MM-DDTHH:mm)同样接受。
func TestInboxAcceptsDateTimeLocalWithoutSeconds(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/inbox?account_id=acc_1&start=2026-08-01T08:30&end=2026-08-10T18:45", nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	got := f.listInboxQuery.DateRange
	if got.Start.Hour() != 8 || got.Start.Minute() != 30 {
		t.Fatalf("Start = %v, want 08:30", got.Start)
	}
	if got.End.Hour() != 18 || got.End.Minute() != 45 {
		t.Fatalf("End = %v, want 18:45", got.End)
	}
}
