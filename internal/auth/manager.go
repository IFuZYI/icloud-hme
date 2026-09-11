// Package auth 实现管理员密码校验、内存会话、CSRF 与登录限流。
//
// 不依赖 Gin:HTTP 中间件只负责 Cookie/Header 与 HTTP 状态映射。
// 会话只存内存,进程重启即失效;session ID 与 CSRF 永不落盘或写日志。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	// DefaultTTL 是默认会话有效期。
	DefaultTTL = 12 * time.Hour
	// maxSessions 是同时有效会话上限,超出时淘汰最早过期的会话。
	maxSessions = 32
)

// Options 是 Manager 的构造选项。
type Options struct {
	Password string
	TTL      time.Duration
	Now      func() time.Time
	Random   io.Reader
}

// Session 是一次管理员会话的公开信息。
type Session struct {
	CSRFToken string
	ExpiresAt time.Time
}

// sessionRecord 是内存会话记录,map key 为 session ID 的 SHA-256。
type sessionRecord struct {
	csrfHash  [32]byte
	csrfToken string
	expiresAt time.Time
}

// Manager 管理管理员会话,线程安全。
type Manager struct {
	mu       sync.Mutex
	salt     []byte
	password []byte
	ttl      time.Duration
	now      func() time.Time
	random   io.Reader
	sessions map[[32]byte]sessionRecord
}

// NewManager 创建会话管理器。
//
// 拒绝空密码与短于 8 字符的密码;启动时用随机 salt + argon2.IDKey
// 派生密码,登录时使用常量时间比较。
func NewManager(opts Options) (*Manager, error) {
	if len(opts.Password) < 8 {
		return nil, errors.New("管理员密码长度不能少于 8 个字符")
	}
	if opts.TTL <= 0 {
		opts.TTL = DefaultTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}

	salt := make([]byte, 16)
	if _, err := io.ReadFull(opts.Random, salt); err != nil {
		return nil, fmt.Errorf("生成密码 salt 失败: %w", err)
	}
	// argon2id: time=1, memory=64*1024 KiB, threads=4, keyLen=32
	derived := argon2.IDKey([]byte(opts.Password), salt, 1, 64*1024, 4, 32)

	return &Manager{
		salt:     salt,
		password: derived,
		ttl:      opts.TTL,
		now:      opts.Now,
		random:   opts.Random,
		sessions: make(map[[32]byte]sessionRecord),
	}, nil
}

// Login 校验管理员密码;成功时创建会话并返回 session ID。
func (m *Manager) Login(password string) (sessionID string, session Session, ok bool) {
	derived := argon2.IDKey([]byte(password), m.salt, 1, 64*1024, 4, 32)
	if subtle.ConstantTimeCompare(derived, m.password) != 1 {
		return "", Session{}, false
	}

	idBytes := make([]byte, 32)
	if _, err := io.ReadFull(m.random, idBytes); err != nil {
		return "", Session{}, false
	}
	csrf := make([]byte, 32)
	if _, err := io.ReadFull(m.random, csrf); err != nil {
		return "", Session{}, false
	}

	sessionID = base64.RawURLEncoding.EncodeToString(idBytes)
	token := base64.RawURLEncoding.EncodeToString(csrf)
	expiresAt := m.now().Add(m.ttl)

	key := sha256.Sum256([]byte(sessionID))
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	// 会话数达到上限时淘汰最早过期的会话,防止内存无界增长
	if len(m.sessions) >= maxSessions {
		m.evictOldestLocked()
	}
	if _, exists := m.sessions[key]; exists {
		// 碰撞概率可忽略;保守起见拒绝本次登录
		return "", Session{}, false
	}
	m.sessions[key] = sessionRecord{
		csrfHash:  sha256.Sum256([]byte(token)),
		csrfToken: token,
		expiresAt: expiresAt,
	}

	return sessionID, Session{CSRFToken: token, ExpiresAt: expiresAt}, true
}

// Validate 校验会话是否有效;每次调用先清理过期会话。
func (m *Manager) Validate(sessionID string) (Session, bool) {
	key := sha256.Sum256([]byte(sessionID))
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	rec, exists := m.sessions[key]
	if !exists {
		return Session{}, false
	}
	return Session{CSRFToken: rec.csrfToken, ExpiresAt: rec.expiresAt}, true
}

// ValidateCSRF 校验 CSRF token(常量时间比较)。
func (m *Manager) ValidateCSRF(sessionID, token string) bool {
	key := sha256.Sum256([]byte(sessionID))
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	rec, exists := m.sessions[key]
	if !exists {
		return false
	}
	want := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(rec.csrfHash[:], want[:]) == 1
}

// Logout 删除会话。
func (m *Manager) Logout(sessionID string) {
	key := sha256.Sum256([]byte(sessionID))
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, key)
}

// pruneLocked 清理所有过期会话,须持锁调用。
func (m *Manager) pruneLocked() {
	now := m.now()
	for key, rec := range m.sessions {
		if !now.Before(rec.expiresAt) {
			delete(m.sessions, key)
		}
	}
}

// evictOldestLocked 淘汰最早过期的会话,须持锁调用。
func (m *Manager) evictOldestLocked() {
	oldest := time.Time{}
	var oldestKey [32]byte
	for key, rec := range m.sessions {
		if oldest.IsZero() || rec.expiresAt.Before(oldest) {
			oldest = rec.expiresAt
			oldestKey = key
		}
	}
	if !oldest.IsZero() {
		delete(m.sessions, oldestKey)
	}
}
