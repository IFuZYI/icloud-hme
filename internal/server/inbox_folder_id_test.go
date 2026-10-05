package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"icloud-hme/internal/account"
	"icloud-hme/internal/mail"
)

// 文件夹限定的邮件 ID("Junk:1")必须能穿过 Gin 路由(URL 编码)到达后端,
// 并在 previews/get/delete 三个入口原样透传。
func TestFolderQualifiedMessageIDs(t *testing.T) {
	f := &fakeBackend{
		accounts: []account.Summary{{ID: "acc_1", Name: "主号"}},
		inbox: InboxResult{
			Method: "imap",
			Messages: []mail.Message{
				{ID: "1", Subject: "收件箱邮件", Folder: "INBOX"},
				{ID: "Junk:1", Subject: "垃圾箱验证码", Folder: "Junk"},
			},
		},
	}
	_, ts := newTestServer(t, f)
	cookie, csrf := login(t, ts, "admin-pass-2026-strong")

	// previews: "Junk:1" 原样到达后端。
	req := authedReq(t, ts, "POST", "/api/inbox/previews?account_id=acc_1", `{"ids":["Junk:1","1"]}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("previews status = %d, body = %s", code, raw)
	}
	var prevResp struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &prevResp); err != nil {
		t.Fatal(err)
	}
	if prevResp.Data["Junk:1"] == "" {
		t.Fatalf("previews 应包含 Junk:1, got %v", prevResp.Data)
	}

	// get: URL 编码的 Junk%3A1 被正确解码并透传。
	req = authedReq(t, ts, "GET", "/api/inbox/Junk%3A1?account_id=acc_1", "")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	code, raw, _ = do(t, req)
	if code != 200 {
		t.Fatalf("get Junk:1 status = %d, body = %s", code, raw)
	}
	var getResp struct {
		Data mail.FullMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &getResp); err != nil {
		t.Fatal(err)
	}
	if getResp.Data.ID != "Junk:1" {
		t.Fatalf("get 返回 ID = %q, want Junk:1", getResp.Data.ID)
	}

	// delete: 同样透传。
	req = authedReq(t, ts, "DELETE", "/api/inbox/Junk%3A1?account_id=acc_1", "")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	code, raw, _ = do(t, req)
	if code != 200 {
		t.Fatalf("delete Junk:1 status = %d, body = %s", code, raw)
	}

	// 非法 ID 拒绝(既非 uid 也非 folder:uid)。
	req = authedReq(t, ts, "GET", "/api/inbox/not-a-uid?account_id=acc_1", "")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	code, _, _ = do(t, req)
	if code != 400 {
		t.Fatalf("非法 ID 应 400, got %d", code)
	}
}
