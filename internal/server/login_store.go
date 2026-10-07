package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
)

// LoginSession 是一次进行中的 iCloud 密码登录。
//
// 2FA 场景下 Begin 返回 hme.ErrOTPRequired,之后对同一会话调用 CompleteOTP /
// CompleteSMS;成功后由 Summary 把结果落库并返回脱敏摘要。
// 该接口让 server 层不直接依赖 *hme.Client,便于测试注入脚本化会话。
type LoginSession interface {
	Begin(password string) error
	// PrepareDelivery 决定验证码投递方式并发起投递, 返回
	// trusted_devices / sms / sms_selection_required。
	PrepareDelivery() (string, error)
	// Delivery 返回当前会话的验证码投递方式(未决定时为空串)。
	Delivery() string
	CompleteOTP(code string) error
	CompleteSMS(phoneID int, code string) error
	ResendOTP() error
	TrustedPhones() ([]hme.TrustedPhone, error)
	SendSMS(phoneID int) error
	Summary() (account.Summary, error)
}

// loginTTL 登录会话有效期。验证码通常几秒内送达,5 分钟足够。
const loginTTL = 5 * time.Minute

// lockedSession 用互斥锁串行化同一登录会话的所有方法调用。
//
// 同一 session_id 的并发请求(双击重试/网络重发)会同时驱动同一个 hme.Client:
// authState 无锁读写、对 Apple 双发请求, 且 409 后两端各自推进状态机。
// 经 store 取出的会话都是本包装, 上游调用因此自动互斥。
type lockedSession struct {
	mu  sync.Mutex
	raw LoginSession
}

func (l *lockedSession) Begin(password string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.Begin(password)
}

func (l *lockedSession) PrepareDelivery() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.PrepareDelivery()
}

func (l *lockedSession) Delivery() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.Delivery()
}

func (l *lockedSession) CompleteOTP(code string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.CompleteOTP(code)
}

func (l *lockedSession) CompleteSMS(phoneID int, code string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.CompleteSMS(phoneID, code)
}

func (l *lockedSession) ResendOTP() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.ResendOTP()
}

func (l *lockedSession) TrustedPhones() ([]hme.TrustedPhone, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.TrustedPhones()
}

func (l *lockedSession) SendSMS(phoneID int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.SendSMS(phoneID)
}

func (l *lockedSession) Summary() (account.Summary, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw.Summary()
}

// loginEntry 一个等待 2FA 的登录会话。
type loginEntry struct {
	accountID string
	session   LoginSession
	expiresAt time.Time
}

// loginStore 保存两段式登录的中间状态(内存,不落盘)。
// 会话在成功完成时被消费;提交失败时保留,允许用户重输验证码。
type loginStore struct {
	mu      sync.Mutex
	entries map[string]*loginEntry
}

func newLoginStore() *loginStore {
	return &loginStore{entries: map[string]*loginEntry{}}
}

// put 保存会话并返回一次性 session ID。
func (s *loginStore) put(accountID string, session LoginSession) string {
	id := newLoginSessionID()
	s.mu.Lock()
	s.gcLocked(time.Now())
	s.entries[id] = &loginEntry{accountID: accountID, session: &lockedSession{raw: session}, expiresAt: time.Now().Add(loginTTL)}
	s.mu.Unlock()
	return id
}

// peek 查看会话但不消费(用于查手机号、发短信等辅助操作)。
func (s *loginStore) peek(id string) (string, LoginSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return "", nil, fmt.Errorf("登录会话不存在或已过期,请重新发起登录")
	}
	if time.Now().After(e.expiresAt) {
		// 惰性清理: 无新会话写入时过期条目不再永久驻留。
		delete(s.entries, id)
		return "", nil, fmt.Errorf("登录会话不存在或已过期,请重新发起登录")
	}
	return e.accountID, e.session, nil
}

// consume 取出并移除会话(验证码校验成功、登录完成时调用)。
func (s *loginStore) consume(id string) (string, LoginSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return "", nil, fmt.Errorf("登录会话不存在或已过期,请重新发起登录")
	}
	if time.Now().After(e.expiresAt) {
		delete(s.entries, id)
		return "", nil, fmt.Errorf("登录会话不存在或已过期,请重新发起登录")
	}
	delete(s.entries, id)
	return e.accountID, e.session, nil
}

// gcLocked 清理过期会话,避免内存滞留。调用方必须持有锁。
func (s *loginStore) gcLocked(now time.Time) {
	for id, e := range s.entries {
		if now.After(e.expiresAt) {
			delete(s.entries, id)
		}
	}
}

func newLoginSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
