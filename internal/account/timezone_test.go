package account

import (
	"context"
	"errors"
	"testing"
)

// fakeResolver 是脚本化的地理解析器。
type fakeResolver struct {
	tz      string
	err     error
	calls   int
	lastURL string
}

func (f *fakeResolver) TimezoneFor(_ context.Context, proxyURL string) (string, error) {
	f.calls++
	f.lastURL = proxyURL
	return f.tz, f.err
}

// TimezoneFor: 无代理返回空串(调用方回退本地时区), 不发起解析。
func TestTimezoneForWithoutProxyReturnsEmpty(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fakeResolver{tz: "America/Denver"}
	m.tzResolver = resolver
	acc, err := m.AddAccount("测试", "", "icloud.com", "")
	if err != nil {
		t.Fatal(err)
	}
	tz, err := m.TimezoneFor(acc.ID)
	if err != nil || tz != "" {
		t.Fatalf("无代理应返回空串: tz=%q err=%v", tz, err)
	}
	if resolver.calls != 0 {
		t.Fatalf("无代理不应发起解析, got %d 次", resolver.calls)
	}
}

// TimezoneFor: 首次解析并缓存; 第二次调用命中缓存不再解析。
func TestTimezoneForResolvesOnceAndCaches(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fakeResolver{tz: "America/Denver"}
	m.tzResolver = resolver
	acc, err := m.AddAccount("测试", "", "icloud.com", "http://user:pass@proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	tz, err := m.TimezoneFor(acc.ID)
	if err != nil || tz != "America/Denver" {
		t.Fatalf("首次解析 = %q, %v", tz, err)
	}
	if resolver.calls != 1 {
		t.Fatalf("首次应解析一次, got %d", resolver.calls)
	}
	// 第二次: 命中缓存。
	tz2, err := m.TimezoneFor(acc.ID)
	if err != nil || tz2 != "America/Denver" {
		t.Fatalf("缓存读取 = %q, %v", tz2, err)
	}
	if resolver.calls != 1 {
		t.Fatalf("缓存命中不应再解析, got %d 次", resolver.calls)
	}
	// 重启: 缓存应已持久化, 重载后不再解析。
	m2, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolver2 := &fakeResolver{tz: "Europe/Berlin"}
	m2.tzResolver = resolver2
	tz3, err := m2.TimezoneFor(acc.ID)
	if err != nil || tz3 != "America/Denver" {
		t.Fatalf("重载后应读持久化缓存 = %q, %v", tz3, err)
	}
	if resolver2.calls != 0 {
		t.Fatalf("重载后不应重新解析, got %d 次", resolver2.calls)
	}
}

// TimezoneFor: 代理变更时清空缓存, 下次调用按新代理重新解析。
func TestTimezoneCacheClearedOnProxyChange(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fakeResolver{tz: "America/Denver"}
	m.tzResolver = resolver
	acc, err := m.AddAccount("测试", "", "icloud.com", "http://user:pass@proxy-a.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.TimezoneFor(acc.ID); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 {
		t.Fatalf("首次解析次数 = %d", resolver.calls)
	}

	// 换代理: 缓存必须失效, 新解析用新代理。
	resolver.tz = "Europe/Berlin"
	if _, err := m.UpdateProxy(acc.ID, "http://user:pass@proxy-b.example:8080"); err != nil {
		t.Fatal(err)
	}
	tz, err := m.TimezoneFor(acc.ID)
	if err != nil || tz != "Europe/Berlin" {
		t.Fatalf("换代理后应重新解析 = %q, %v", tz, err)
	}
	if resolver.calls != 2 {
		t.Fatalf("换代理应再解析一次, got %d", resolver.calls)
	}
	if resolver.lastURL != "http://user:pass@proxy-b.example:8080" {
		t.Fatalf("解析应使用新代理, got %q", resolver.lastURL)
	}
}

// TimezoneFor: 解析失败返回错误(调用方回退本地时区), 不写缓存。
func TestTimezoneForResolveFailureReturnsError(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fakeResolver{err: errors.New("全部地理解析服务失败")}
	m.tzResolver = resolver
	acc, err := m.AddAccount("测试", "", "icloud.com", "http://user:pass@proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.TimezoneFor(acc.ID); err == nil {
		t.Fatal("解析失败应返回错误")
	}
	// 失败不写缓存: 修好解析器后应能成功。
	resolver.err = nil
	resolver.tz = "Asia/Tokyo"
	tz, err := m.TimezoneFor(acc.ID)
	if err != nil || tz != "Asia/Tokyo" {
		t.Fatalf("失败后重试应成功 = %q, %v", tz, err)
	}
}
