package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/apikey"
	"icloud-hme/internal/mailtest"
)

// ====================================================================
// 邮件清理任务的「延迟权限」修复
// ====================================================================
//
// 契约:
//  1. 创建任务时记录创建者秘钥(CreatedByKeyID; 会话创建为空);
//  2. runner 在**每个 chunk 删除前**用 keyGuard 重校该秘钥:
//     秘钥被删除/停用 → key_revoked; 失去 inbox.purge →
//     key_permission_revoked; 账号移出范围 → key_scope_revoked;
//     校验失败 → 任务 paused + PauseReason + warn 日志, 且删除计数为 0;
//  3. 因权限失效暂停的任务一律拒绝 resume(manager 哨兵
//     ErrResumeDeniedByPolicy; handler 409 PERMISSION_DENIED), 会话亦不例外;
//  4. pause/resume/cancel 加所有权校验: 秘钥只能操作自己创建的任务,
//     会话不受限;
//  5. keyGuard 为 nil 时行为与旧版一致(向后兼容)。

// newPurgeAuthzHarness 组装带执行期秘钥校验的清理测试环境。
//
// purgeChunkSize 调小到 2: 只要 runner 多删一封, Deleted 就会 > 0,
// 使「权限失效后一封都没删」的断言足够敏感。
func newPurgeAuthzHarness(t *testing.T, n int) (*Server, *httptestServer, *mailtest.Server) {
	t.Helper()
	oldChunk := purgeChunkSize
	purgeChunkSize = 2
	t.Cleanup(func() { purgeChunkSize = oldChunk })
	s, hts, ms := newPurgeHTTPHarness(t, n)
	// 与生产接线一致: 基于秘钥管理器构造执行期校验器。
	s.purge.SetKeyGuard(purgeKeyGuard(s.keys))
	return s, hts, ms
}

// mustCreatePurgeTaskFor 直接经 manager 建任务(可指定创建者秘钥)。
func mustCreatePurgeTaskFor(t *testing.T, s *Server, accountID, keyID string) PurgeTask {
	t.Helper()
	tk, err := s.purge.createWithKey(accountID, "账号 "+accountID,
		time.Now().AddDate(0, 0, -30), time.Now(), keyID)
	if err != nil {
		t.Fatalf("createWithKey(%s, key=%q): %v", accountID, keyID, err)
	}
	return tk
}

// assertPurgePausedNoDelete 断言任务因权限失效暂停且一封未删。
func assertPurgePausedNoDelete(t *testing.T, s *Server, ms *mailtest.Server, taskID, wantReason string) PurgeTask {
	t.Helper()
	got, ok := s.purge.get(taskID)
	if !ok {
		t.Fatalf("任务 %s 不存在", taskID)
	}
	if got.Status != "paused" {
		t.Fatalf("状态 = %q, want paused (%+v)", got.Status, got)
	}
	if got.PauseReason != wantReason {
		t.Fatalf("PauseReason = %q, want %q (%+v)", got.PauseReason, wantReason, got)
	}
	if got.Deleted != 0 || got.Processed != 0 {
		t.Fatalf("权限失效后不得删除邮件: deleted=%d processed=%d", got.Deleted, got.Processed)
	}
	// 端到端证据: 一次 STORE/EXPUNGE 都不应发生(SEARCH 是只读的)。
	if n := ms.StoreCount(); n != 0 {
		t.Fatalf("不应发出任何 STORE, got %d", n)
	}
	if n := ms.ExpungeCount(); n != 0 {
		t.Fatalf("不应发出任何 EXPUNGE, got %d", n)
	}
	return got
}

// purgeTaskLogs 返回某任务的日志快照。
func purgeTaskLogs(t *testing.T, s *Server, taskID string) []AliasTaskLog {
	t.Helper()
	s.task.mu.Lock()
	defer s.task.mu.Unlock()
	var out []AliasTaskLog
	for _, l := range s.task.logs {
		if l.TaskID == taskID {
			out = append(out, l)
		}
	}
	return out
}

// sessionPurgeAction 用管理员会话调用 pause/resume/cancel(带 CSRF)。
func sessionPurgeAction(t *testing.T, hts *httptestServer, session, csrf, action, id string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, hts.ts.URL+"/api/purge-tasks/"+id+"/"+action, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, raw, _ := do(t, req)
	return code, raw
}

// keyPurgeAction 用 API 秘钥调用 pause/resume/cancel。
func keyPurgeAction(t *testing.T, hts *httptestServer, apiKey, action, id string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, hts.ts.URL+"/api/purge-tasks/"+id+"/"+action, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", apiKey)
	code, raw, _ := do(t, req)
	return code, raw
}

// a) 创建者秘钥被删除 / 停用 → 跑一块即 paused(key_revoked), 删除计数 0。
func TestPurgeAuthzKeyRevokedPauses(t *testing.T) {
	cases := []struct {
		name   string
		revoke func(t *testing.T, s *Server, key apikey.Key)
	}{
		{"删除", func(t *testing.T, s *Server, key apikey.Key) {
			if err := s.keys.Delete(key.ID); err != nil {
				t.Fatalf("删除秘钥: %v", err)
			}
		}},
		{"停用", func(t *testing.T, s *Server, key apikey.Key) {
			disabled := false
			if _, err := s.keys.Update(key.ID, apikey.UpdateInput{Enabled: &disabled}); err != nil {
				t.Fatalf("停用秘钥: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, ms := newPurgeAuthzHarness(t, 5)
			key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
			tk := mustCreatePurgeTaskFor(t, s, "acc_1", key.ID)

			tc.revoke(t, s, key)
			s.purge.runSync(tk.ID)

			assertPurgePausedNoDelete(t, s, ms, tk.ID, purgePauseReasonKeyRevoked)

			// 任务日志: category purge + level warn + 说明暂停原因。
			var found bool
			for _, l := range purgeTaskLogs(t, s, tk.ID) {
				if l.Category == "purge" && l.Level == "warn" &&
					strings.Contains(l.Message, purgePauseReasonKeyRevoked) {
					found = true
				}
			}
			if !found {
				t.Fatalf("应写一条 purge/warn 日志说明暂停原因, logs=%+v",
					purgeTaskLogs(t, s, tk.ID))
			}
		})
	}
}

// b) 秘钥仍在但失去 inbox.purge → paused(key_permission_revoked)。
func TestPurgeAuthzKeyPermissionRevoked(t *testing.T) {
	s, _, ms := newPurgeAuthzHarness(t, 5)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
	tk := mustCreatePurgeTaskFor(t, s, "acc_1", key.ID)

	perms := []string{"inbox.list"} // 只剩读权限
	if _, err := s.keys.Update(key.ID, apikey.UpdateInput{Permissions: &perms}); err != nil {
		t.Fatalf("降权秘钥: %v", err)
	}
	s.purge.runSync(tk.ID)

	assertPurgePausedNoDelete(t, s, ms, tk.ID, purgePauseReasonKeyPermissionRevoked)
}

// c) 账号被移出秘钥范围 → paused(key_scope_revoked)。
func TestPurgeAuthzKeyScopeRevoked(t *testing.T) {
	s, _, ms := newPurgeAuthzHarness(t, 5)
	key := createTestKeyScoped(t, s, []string{"acc_1", "acc_2"}, "inbox.purge")
	tk := mustCreatePurgeTaskFor(t, s, "acc_1", key.ID)

	scope := []string{"acc_2"} // acc_1 被移出范围
	if _, err := s.keys.Update(key.ID, apikey.UpdateInput{AccountIDs: &scope}); err != nil {
		t.Fatalf("缩范围: %v", err)
	}
	s.purge.runSync(tk.ID)

	assertPurgePausedNoDelete(t, s, ms, tk.ID, purgePauseReasonKeyScopeRevoked)
}

// d) 权限失效暂停的任务拒绝 resume: manager 返回哨兵错误, HTTP(含会话)409。
func TestPurgeAuthzResumeDeniedByPolicy(t *testing.T) {
	s, hts, _ := newPurgeAuthzHarness(t, 5)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
	tk := mustCreatePurgeTaskFor(t, s, "acc_1", key.ID)
	s.purge.pauseWithPolicyReason(tk.ID, purgePauseReasonKeyPermissionRevoked)

	// manager 层: 三种权限失效原因都返回哨兵错误。
	for _, reason := range []string{
		purgePauseReasonKeyRevoked,
		purgePauseReasonKeyPermissionRevoked,
		purgePauseReasonKeyScopeRevoked,
	} {
		s.purge.pauseWithPolicyReason(tk.ID, reason)
		if err := s.purge.resume(tk.ID); !errors.Is(err, ErrResumeDeniedByPolicy) {
			t.Fatalf("reason=%s resume = %v, want ErrResumeDeniedByPolicy", reason, err)
		}
		got, _ := s.purge.get(tk.ID)
		if got.Status != "paused" || got.PauseReason != reason {
			t.Fatalf("resume 被拒后任务不得变化: %+v", got)
		}
	}

	// 会话(管理员)也适用: 任务必须用新秘钥重建 → 409 PERMISSION_DENIED。
	session, csrf := login(t, hts.ts, "admin-pass-2026-strong")
	code, raw := sessionPurgeAction(t, hts, session, csrf, "resume", tk.ID)
	if code != http.StatusConflict || !strings.Contains(raw, "PERMISSION_DENIED") {
		t.Fatalf("会话 resume 应 409 PERMISSION_DENIED, got %d %s", code, raw)
	}
	if !strings.Contains(raw, "请用新秘钥重新创建清理任务") {
		t.Fatalf("409 消息应提示重建任务: %s", raw)
	}
	got, _ := s.purge.get(tk.ID)
	if got.Status != "paused" {
		t.Fatalf("409 后任务应仍 paused, got %q", got.Status)
	}
}

// e) 同范围另一把秘钥 pause/cancel → 403; 会话调用 → 200。
func TestPurgeAuthzForeignKeyCannotAct(t *testing.T) {
	s, hts, _ := newPurgeAuthzHarness(t, 5)
	owner := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
	other := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
	tk := mustCreatePurgeTaskFor(t, s, "acc_1", owner.ID)

	for _, action := range []string{"pause", "cancel"} {
		code, raw := keyPurgeAction(t, hts, other.Key, action, tk.ID)
		if code != http.StatusForbidden || !strings.Contains(raw, "PERMISSION_DENIED") {
			t.Fatalf("%s 由同范围其它秘钥调用应 403, got %d %s", action, code, raw)
		}
		if !strings.Contains(raw, "只能操作本秘钥创建的任务") {
			t.Fatalf("403 消息应说明所有权: %s", raw)
		}
		got, _ := s.purge.get(tk.ID)
		if got.Status != "running" {
			t.Fatalf("%s 被拒后任务不得变化, got %q", action, got.Status)
		}
	}

	// 会话不受所有权限制 → 200。
	session, csrf := login(t, hts.ts, "admin-pass-2026-strong")
	code, raw := sessionPurgeAction(t, hts, session, csrf, "pause", tk.ID)
	if code != http.StatusOK {
		t.Fatalf("会话 pause 应 200, got %d %s", code, raw)
	}
	got, _ := s.purge.get(tk.ID)
	if got.Status != "paused" {
		t.Fatalf("会话 pause 后状态 = %q, want paused", got.Status)
	}

	// 创建者秘钥仍可操作自己的任务。
	code, raw = keyPurgeAction(t, hts, owner.Key, "cancel", tk.ID)
	if code != http.StatusOK {
		t.Fatalf("创建者 cancel 自己的任务应 200, got %d %s", code, raw)
	}
	got, _ = s.purge.get(tk.ID)
	if got.Status != "cancelled" {
		t.Fatalf("cancel 后状态 = %q, want cancelled", got.Status)
	}
}

// f) 会话创建的任务(CreatedByKeyID 为空)正常跑完, 不受守卫影响。
func TestPurgeAuthzSessionTaskUnaffected(t *testing.T) {
	s, _, _ := newPurgeAuthzHarness(t, 5)
	// 守卫已装 + 存在一把已删除的秘钥: 会话任务不应受任何影响。
	k := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
	if err := s.keys.Delete(k.ID); err != nil {
		t.Fatal(err)
	}
	tk := mustCreatePurgeTaskFor(t, s, "acc_1", "")
	s.purge.runSync(tk.ID)

	got, _ := s.purge.get(tk.ID)
	if got.Status != "done" {
		t.Fatalf("会话任务应跑完, got status=%q reason=%q err=%q", got.Status, got.PauseReason, got.Error)
	}
	if got.Deleted != 5 {
		t.Fatalf("会话任务应删除 5 封, got %d", got.Deleted)
	}
	if got.PauseReason != "" {
		t.Fatalf("会话任务不应有暂停原因, got %q", got.PauseReason)
	}
}

// 创建接口记录创建者秘钥(秘钥请求), 会话创建为空。
func TestPurgeAuthzCreateRecordsCreator(t *testing.T) {
	s, hts, _ := newPurgeAuthzHarness(t, 3)
	// 关闭执行器: 本用例只校验创建时落库的创建者字段, 不跑真实删除。
	s.purge.withClient = nil
	key := createTestKeyScoped(t, s, []string{"acc_1", "acc_2"}, "inbox.purge")

	body := `{"account_id":"acc_1","range_start":"2020-01-01T00:00:00Z","range_end":"2030-01-01T00:00:00Z"}`
	req, _ := http.NewRequest(http.MethodPost, hts.ts.URL+"/api/inbox/purge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key.Key)
	code, raw, _ := do(t, req)
	if code != http.StatusOK {
		t.Fatalf("秘钥创建任务应 200, got %d %s", code, raw)
	}
	var out struct {
		Data PurgeTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("解析响应: %v (%s)", err, raw)
	}
	if out.Data.CreatedByKeyID != key.ID {
		t.Fatalf("CreatedByKeyID = %q, want %q", out.Data.CreatedByKeyID, key.ID)
	}
	got, ok := s.purge.get(out.Data.ID)
	if !ok || got.CreatedByKeyID != key.ID {
		t.Fatalf("任务落库的创建者 = %+v (ok=%v)", got, ok)
	}

	// 会话创建: 创建者为空。
	session, csrf := login(t, hts.ts, "admin-pass-2026-strong")
	body2 := `{"account_id":"acc_2","range_start":"2020-01-01T00:00:00Z","range_end":"2030-01-01T00:00:00Z"}`
	req2, _ := http.NewRequest(http.MethodPost, hts.ts.URL+"/api/inbox/purge", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-CSRF-Token", csrf)
	req2.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, raw, _ = do(t, req2)
	if code != http.StatusOK {
		t.Fatalf("会话创建任务应 200, got %d %s", code, raw)
	}
	var out2 struct {
		Data PurgeTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &out2); err != nil {
		t.Fatalf("解析响应: %v (%s)", err, raw)
	}
	if out2.Data.CreatedByKeyID != "" {
		t.Fatalf("会话任务的 CreatedByKeyID 应为空, got %q", out2.Data.CreatedByKeyID)
	}
}

// keyGuard 为 nil 时保持旧行为(向后兼容): 即使创建者秘钥已删除, 任务照旧跑完。
func TestPurgeAuthzNilGuardLegacyBehavior(t *testing.T) {
	s, _, _ := newPurgeAuthzHarness(t, 5)
	s.purge.SetKeyGuard(nil)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
	tk := mustCreatePurgeTaskFor(t, s, "acc_1", key.ID)
	if err := s.keys.Delete(key.ID); err != nil {
		t.Fatal(err)
	}
	s.purge.runSync(tk.ID)
	got, _ := s.purge.get(tk.ID)
	if got.Status != "done" || got.Deleted != 5 {
		t.Fatalf("nil 守卫应保持旧行为(跑完), got status=%q deleted=%d", got.Status, got.Deleted)
	}
}

// 权限失效暂停会释放账号锁: 用户能用新秘钥重建任务(否则 409 死锁),
// 且随后取消旧任务不得误删新任务持有的锁。
func TestPurgeAuthzRevokedTaskReleasesAccountForRecreate(t *testing.T) {
	s, hts, ms := newPurgeAuthzHarness(t, 5)
	oldKey := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
	old := mustCreatePurgeTaskFor(t, s, "acc_1", oldKey.ID)
	if err := s.keys.Delete(oldKey.ID); err != nil {
		t.Fatal(err)
	}
	s.purge.runSync(old.ID)
	assertPurgePausedNoDelete(t, s, ms, old.ID, purgePauseReasonKeyRevoked)

	// 同账号可用新秘钥重建(旧任务已让出账号锁)。
	newKey := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
	rebuilt, err := s.purge.createWithKey("acc_1", "账号 acc_1",
		time.Now().AddDate(0, 0, -30), time.Now(), newKey.ID)
	if err != nil {
		t.Fatalf("吊销后应能用新秘钥重建任务: %v", err)
	}

	// 会话取消旧的权限失效任务: 不得释放新任务持有的账号锁。
	session, csrf := login(t, hts.ts, "admin-pass-2026-strong")
	if code, raw := sessionPurgeAction(t, hts, session, csrf, "cancel", old.ID); code != http.StatusOK {
		t.Fatalf("取消旧任务应 200, got %d %s", code, raw)
	}
	s.purge.mu.Lock()
	lockOwner := s.purge.active["acc_1"]
	s.purge.mu.Unlock()
	if lockOwner != rebuilt.ID {
		t.Fatalf("取消旧任务误删了新任务的账号锁: owner=%q want %q", lockOwner, rebuilt.ID)
	}

	s.purge.runSync(rebuilt.ID)
	got, _ := s.purge.get(rebuilt.ID)
	if got.Status != "done" || got.Deleted != 5 {
		t.Fatalf("新任务应正常跑完, got status=%q deleted=%d", got.Status, got.Deleted)
	}
}
