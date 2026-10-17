package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// ====================================================================
// 尾随 JSON 解析差异: 范围校验必须与 handler 的解析语义一致
// ====================================================================
//
// 背景(独立审查实测确认): 中间件 accountIDFromBody 曾用 json.Unmarshal
// 探测 body 的 account_id——它要求整个 body 是单一 JSON 值, 任何尾随数据
// 都让解析失败并返回 ""(等于「没有声明账号」, 跳过范围校验); 而 handler
// 用 gin 的 ShouldBindJSON(内部 json.Decoder.Decode)只读**第一个** JSON
// 值、忽略尾随。
//
// 攻击: 范围受限秘钥(白名单 acc_1)发送
//
//	{"account_id":"acc_2", ...}{"account_id":"acc_1"}
//
// 中间件 Unmarshal 整体失败 → 认为无 account_id → 放行; handler Decode 读到
// 第一个值 → 在 acc_2(范围外)上执行创建/删除。审查实测 6+ 端点受影响。
//
// 契约: 范围校验必须覆盖 body 中**所有** JSON 值声明的账号(任一越界即拒)。

// 尾随 JSON 的创建任务请求: 第一个值声明范围外账号。
// 修复前: 中间件跳过校验, 任务创建在 acc_2 (201) —— RED。
// 修复后: 中间件按 handler 语义解析出 acc_2, 403 ACCOUNT_NOT_ALLOWED。
func TestScopedKeyTrailingJSONTaskCreateBypass(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"account_id":"acc_2","mode":"auto","daily_limit":5,"target_count":5}{"account_id":"acc_1"}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks", key.Key, body)
	code, raw, _ := do(t, req)

	if code != 403 {
		t.Fatalf("尾随 JSON 越权创建任务应 403, got %d %s", code, raw)
	}
	if !strings.Contains(raw, "ACCOUNT_NOT_ALLOWED") {
		t.Fatalf("错误码应为 ACCOUNT_NOT_ALLOWED: %s", raw)
	}
	// 任务不得真的创建到范围外账号。
	for _, tk := range s.task.list() {
		if tk.AccountID == "acc_2" {
			t.Fatalf("范围外账号不应出现新任务: %+v", tk)
		}
	}
}

// 反向: 第一个值在范围内、尾随值在范围外——handler 只会执行第一个值,
// 但校验必须覆盖「所有」声明来源, 因此同样拒绝(与既有契约一致:
// 任一来源越界即拒)。
func TestScopedKeyTrailingJSONOutOfScopeInTrailerRejected(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"account_id":"acc_1","mode":"auto","daily_limit":5,"target_count":5}{"account_id":"acc_2"}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks", key.Key, body)
	code, raw, _ := do(t, req)

	if code != 403 {
		t.Fatalf("尾随值声明范围外账号应被拒(覆盖全部来源), got %d %s", code, raw)
	}
	if !strings.Contains(raw, "ACCOUNT_NOT_ALLOWED") {
		t.Fatalf("错误码应为 ACCOUNT_NOT_ALLOWED: %s", raw)
	}
}

// 对照组(正向): 单一合法 JSON 且账号在范围内——必须正常创建(201)。
// 防止修复矫枉过正把正常请求也拒掉。
func TestScopedKeySingleJSONInScopeStillWorks(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"account_id":"acc_1","mode":"auto","daily_limit":5,"target_count":5}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks", key.Key, body)
	code, raw, _ := do(t, req)

	if code != 201 {
		t.Fatalf("范围内正常请求应 201, got %d %s", code, raw)
	}
}

// 对照组(正向): 尾随数据但无账号声明——不越界, 按 handler 语义执行
// 第一个值即可(不因存在尾随而误拒)。
func TestScopedKeyTrailingJSONNoAccountStillWorks(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"account_id":"acc_1","mode":"auto","daily_limit":5,"target_count":5}{"pad":1}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks", key.Key, body)
	code, raw, _ := do(t, req)

	if code != 201 {
		t.Fatalf("尾随无账号声明不应误拒, got %d %s", code, raw)
	}
}

// 直接单元验证: accountIDFromBody 的语义必须与 handler 的 Decoder 一致
// (读第一个 JSON 值)。修复前返回 "" (RED), 修复后返回第一个值的 account_id。
func TestAccountIDFromBodyMatchesHandlerSemantics(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"单值", `{"account_id":"acc_1"}`, "acc_1"},
		{"尾随值(第一个声明)", `{"account_id":"acc_2"}{"account_id":"acc_1"}`, "acc_2"},
		{"前导空白", "  \n" + `{"account_id":"acc_3"}`, "acc_3"},
		{"非 JSON", `not json`, ""},
		{"空 body", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 构造最小 gin 上下文: 只喂请求体, 其余字段不需要。
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/api/alias-tasks", strings.NewReader(tc.body))
			got := accountIDFromBody(c)
			if got != tc.want {
				t.Fatalf("accountIDFromBody(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// 尾随值集合: 修复必须覆盖「body 里出现的每一个 account_id」, 不只是第一个。
func TestAccountIDsFromBodyCollectsAllValues(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/alias-tasks",
		strings.NewReader(`{"account_id":"acc_2"}{"account_id":"acc_1"}`))
	got := accountIDsFromBody(c)
	want := []string{"acc_2", "acc_1"}
	if len(got) != len(want) {
		t.Fatalf("应收集全部 account_id: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个 account_id: got %v, want %v", i, got, want)
		}
	}
}

// 去重必须是线性复杂度: 大规模不同 account_id 不得退化为 O(n²)。
//
// 背景(独立质量审查实测): 修复初版 accountScopeTargets 用线性扫描去重,
// 1 MiB body(中间件上限)可塞 ~3.6 万个互不相同的 account_id, 单请求实测
// 3.27s 纯 CPU——鉴权中间件内同步执行, 低成本即可用满 CPU(DoS 面)。
// map 去重后本测试用 5 万个值在毫秒级完成; 线性版本需要 ~1.25e9 次比较,
// 在该规模下必然超时, 因此本测试对「去重实现退化」有区分度。
func TestAccountScopeTargetsDedupIsLinear(t *testing.T) {
	var sb strings.Builder
	const n = 50000
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(" ")
		}
		// 每个值都不同: 线性去重的最坏情形(每次全表扫描)。
		sb.WriteString(`{"account_id":"acc_`)
		sb.WriteString(strings.Repeat("x", 8))
		fmt.Fprintf(&sb, `_%d"}`, i)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/alias-tasks", strings.NewReader(sb.String()))

	start := time.Now()
	got, err := accountScopeTargets(c)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("accountScopeTargets 读取 body 失败: %v", err)
	}
	if len(got) != n {
		t.Fatalf("应收集 %d 个不同账号, got %d", n, len(got))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("去重耗时 %v 超过 2s——疑似退化为 O(n²)(线性扫描去重)", elapsed)
	}
}

// ====================================================================
// 失败关闭: body 读取失败不得跳过账号范围校验
// ====================================================================
//
// 背景(独立质量审查 Important): 修复初版在 io.ReadAll 失败时返回 nil,
// 等于「没有声明账号」跳过 body 范围校验(失败开放), 与包内其余校验刻意
// 失败关闭的约定不一致。修复后读取失败必须向上传播, 中间件直接拒绝。

// errReader 是恒失败的请求体 reader(模拟读错误/连接中断)。
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("simulated read failure") }

// 单元: 读取失败必须传播错误, 不得静默返回空集。
func TestAccountIDsFromBodyReadFailurePropagates(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/alias-tasks", nil)
	c.Request.Body = io.NopCloser(errReader{})

	if _, err := accountIDsFromBodyChecked(c); err == nil {
		t.Fatal("body 读取失败必须返回错误(失败关闭), 实际 nil")
	}

	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest("POST", "/api/alias-tasks", nil)
	c2.Request.Body = io.NopCloser(errReader{})
	if _, err := accountScopeTargets(c2); err == nil {
		t.Fatal("accountScopeTargets 在 body 读取失败时必须返回错误(失败关闭), 实际 nil")
	}
}

// 端到端: chunked 超大 body(ContentLength=-1, 绕开预检)触发 MaxBytesReader,
// 鉴权中间件必须 413 拒绝。
//
// 区分度: 失败开放版本放行到 handler, 由 ShouldBindJSON 报 400; 失败关闭
// 版本在中间件内以 413 拒绝(handler 无法产生 413, 状态码可区分两版行为)。
func TestScopedKeyChunkedOversizeBodyFailsClosed(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	big := `{"account_id":"acc_1","pad":"` + strings.Repeat("x", maxBodyBytes+1024) + `"}`
	req, err := http.NewRequest("POST", ts.URL+"/api/alias-tasks", strings.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1 // 强制 chunked: 绕过 ContentLength 预检, 只能靠 MaxBytesReader 拦截
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key.Key)

	code, raw, _ := do(t, req)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked 超大 body 应在中间件被 413 拒绝(失败关闭), got %d %s", code, raw)
	}
	if !strings.Contains(raw, "请求体过大") {
		t.Fatalf("错误文案应说明请求体过大: %s", raw)
	}
}
