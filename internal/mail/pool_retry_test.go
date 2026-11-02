package mail

import (
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/emersion/go-imap/client"

	"icloud-hme/internal/mailtest"
)

// TestIsLikelyConnErrRecognizesConnectionClosed 验证 go-imap 的连接关闭错误
// 被判定为「连接坏了」。
//
// go-imap 在等待命令响应期间连接被关闭(代理/NAT 静默断开、服务端断开)时返回
// "imap: connection closed"(client.errClosed)。若该错误不在关键字列表里,
// 池就不会丢弃这条坏连接: 后续每次读信都在这条死连接上失败 → 回退 Web API →
// 若 Cookie 也过期就整条读信失败(线上实际形态)。
func TestIsLikelyConnErrRecognizesConnectionClosed(t *testing.T) {
	cases := []string{
		"imap: connection closed",
		"imap: connection closed during command execution",
	}
	for _, msg := range cases {
		if !isLikelyConnErr(errors.New(msg)) {
			t.Fatalf("isLikelyConnErr(%q) 应为 true", msg)
		}
	}
}

// TestPoolRetriesOnceAfterConnClosed 验证借出的连接在命令中途报「连接关闭」时,
// 池会丢弃该连接并在新连接上重试一次, 而不是把错误直接抛给调用方。
//
// 单次失败直接上抛的表现就是用户「收不到邮件」: 读信失败 → 回退 Web API。
// 读信(list/preview/detail)是幂等的, 重试一次不影响语义。
func TestPoolRetriesOnceAfterConnClosed(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	}))
	pool := NewPool()
	t.Cleanup(pool.Close)

	calls := 0
	err := pool.Do("a@icloud.com", "code", "", func(*Client) error {
		calls++
		if calls == 1 {
			return errors.New("imap: connection closed")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("连接关闭后应在新建连接上重试一次, 实际错误 %v", err)
	}
	if calls != 2 {
		t.Fatalf("fn 应被调用 2 次(失败 1 次 + 重试 1 次), 实际 %d", calls)
	}
	if got := srv.ConnCount(); got != 2 {
		t.Fatalf("重试应重新拨号一次, 实际拨号 %d 次", got)
	}
}

// TestPoolDoesNotRetryNonConnError 验证普通业务错误不会被重试(避免副作用重复)。
func TestPoolDoesNotRetryNonConnError(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	}))
	pool := NewPool()
	t.Cleanup(pool.Close)

	calls := 0
	want := errors.New("业务错误: 文件夹不存在")
	err := pool.Do("a@icloud.com", "code", "", func(*Client) error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("业务错误应原样返回, 实际 %v", err)
	}
	if calls != 1 {
		t.Fatalf("业务错误不得重试, 实际调用 %d 次", calls)
	}
}
