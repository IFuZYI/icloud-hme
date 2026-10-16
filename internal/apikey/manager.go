// Package apikey 实现脚本调用用的 API 秘钥: 生成、校验、权限与持久化。
//
// 模型: 本地单管理员工具的「长期访问凭证」。秘钥可替代会话 Cookie +
// CSRF 供脚本调用; 每个秘钥独立权限列表(权限 ID 的合法性由 server 层
// 目录校验, 本包只透传与匹配)。
//
// 存储: data/api_keys.json(0600, writeJSONAtomic 同款原子写)。明文存储
// 与 accounts.json 中的 Cookie/App Password 同级——本工具的数据目录整体
// 是秘密边界, 面板需要可查看秘钥原文, 故不做哈希。
//
// 校验语义: 未知秘钥与已停用秘钥对外一致(调用方只得到「失败」, 不泄露
// 秘钥是否存在); last_used 供面板显示使用情况。
package apikey

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// keyPrefix 是自动生成秘钥的前缀。
	keyPrefix = "sk-"
	// autoKeyBodyLen 是自动生成秘钥的随机部分长度(字符数)。
	autoKeyBodyLen = 24
	// customKeyMinLen/customKeyMaxLen 是手动秘钥的长度边界(字节)。
	customKeyMinLen = 8
	customKeyMaxLen = 128
	// maxKeys 是秘钥数量上限, 防止无界增长。
	maxKeys = 100
	// autoKeyChars 是随机部分的字符集(大小写字母+数字)。
	autoKeyChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	// nameMaxLen 是名称长度上限(rune)。
	nameMaxLen = 64
)

// 错误定义(调用方按 errors.Is 判断)。
var (
	ErrKeyExists   = errors.New("秘钥已存在")
	ErrKeyNotFound = errors.New("秘钥不存在")
	ErrTooManyKeys = errors.New("秘钥数量已达上限")
)

// lastUsedPersistInterval 是 last_used 写盘的节流间隔: 每次校验都写盘会
// 造成高频小 IO; 测试可设为 0(每次立即写)。用 var 而非 const: 测试覆盖。
var lastUsedPersistInterval = time.Minute

// Key 是一条 API 秘钥记录(含明文, 供面板查看/复制)。
type Key struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Key         string   `json:"key"`
	Enabled     bool     `json:"enabled"`
	Permissions []string `json:"permissions"`
	// AccountIDs 是账号使用范围白名单。空 = 不限(全部账号可用);
	// 非空 = 仅列出的账号可用(失败关闭)。omitempty: 空范围序列化为省略。
	AccountIDs []string `json:"account_ids,omitempty"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt string   `json:"last_used_at,omitempty"`
	LastUsedIP string   `json:"last_used_ip,omitempty"`
}

// HasPermission 判断秘钥是否拥有指定权限(失败关闭: 空列表不放行)。
func (k Key) HasPermission(id string) bool {
	for _, p := range k.Permissions {
		if p == id {
			return true
		}
	}
	return false
}

// AllowsAccount 判断秘钥是否被允许访问指定账号(失败关闭)。
//
//   - 空 AccountIDs = 不限账号, 任意账号(含空串)放行;
//   - 非空 AccountIDs = 白名单, 只有明确列出的账号放行, 其余(含空串)拒绝。
func (k Key) AllowsAccount(accountID string) bool {
	if len(k.AccountIDs) == 0 {
		return true
	}
	for _, id := range k.AccountIDs {
		if id == accountID {
			return true
		}
	}
	return false
}

// CreateInput 是创建秘钥的输入。Key 留空时自动生成。
type CreateInput struct {
	Name        string
	Key         string
	Permissions []string
	// AccountIDs 是账号使用范围(空 = 全部账号)。
	AccountIDs []string
}

// UpdateInput 是更新秘钥的输入; nil 字段保持不变(秘钥值不可更新)。
type UpdateInput struct {
	Name        *string
	Permissions *[]string
	// AccountIDs 为 nil 时不变; 指向空切片则清空(= 全部账号)。
	AccountIDs *[]string
	Enabled    *bool
}

// Manager 管理秘钥集合, 线程安全。
type Manager struct {
	mu   sync.Mutex
	file string // 空 = 纯内存模式(测试)
	keys map[string]*Key
	// lastPersist 记录上次因 last_used 写盘的时间(节流)。
	lastPersist time.Time
	now         func() time.Time
	random      func(b []byte) error
}

// NewManager 加载/创建秘钥文件。file 为空时使用纯内存模式。
func NewManager(file string) (*Manager, error) {
	m := &Manager{
		file:   file,
		keys:   make(map[string]*Key),
		now:    time.Now,
		random: func(b []byte) error { _, err := rand.Read(b); return err },
	}
	if file == "" {
		return m, nil
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// load 读取秘钥文件; 不存在视为空集合, 损坏则报错(不静默丢数据)。
func (m *Manager) load() error {
	raw, err := os.ReadFile(m.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var wrapper struct {
		Keys []*Key `json:"keys"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return fmt.Errorf("解析秘钥文件失败: %w", err)
	}
	for _, k := range wrapper.Keys {
		if k == nil || k.ID == "" {
			continue
		}
		m.keys[k.ID] = k
	}
	return nil
}

// saveLocked 原子写盘(0600); 内存模式为空操作。调用方必须持有 m.mu。
func (m *Manager) saveLocked() error {
	if m.file == "" {
		return nil
	}
	keys := make([]*Key, 0, len(m.keys))
	for _, k := range m.keys {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].CreatedAt < keys[j].CreatedAt })
	wrapper := struct {
		Keys      []*Key `json:"keys"`
		UpdatedAt string `json:"updated_at"`
	}{Keys: keys, UpdatedAt: m.now().Format(time.RFC3339)}

	raw, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.file), 0o700); err != nil {
		return err
	}
	tmp := m.file + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.file)
}

// Create 新建秘钥; Key 留空时自动生成 sk-+24 位随机字符。
func (m *Manager) Create(in CreateInput) (Key, error) {
	name := strings.TrimSpace(in.Name)
	if len([]rune(name)) > nameMaxLen {
		return Key{}, fmt.Errorf("名称最长 %d 字符", nameMaxLen)
	}

	key := strings.TrimSpace(in.Key)
	if key == "" {
		generated, err := m.generate()
		if err != nil {
			return Key{}, err
		}
		key = generated
	} else if err := validateCustomKey(key); err != nil {
		return Key{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.keys) >= maxKeys {
		return Key{}, ErrTooManyKeys
	}
	for _, k := range m.keys {
		if k.Key == key {
			return Key{}, ErrKeyExists
		}
	}

	rec := &Key{
		ID:          "key_" + randomHex(m, 8),
		Name:        name,
		Key:         key,
		Enabled:     true,
		Permissions: normalizePermissions(in.Permissions),
		AccountIDs:  normalizeAccountIDs(in.AccountIDs),
		// 纳秒精度: 同一秒内连续创建的秘钥也要有稳定顺序(列表按新→旧)。
		CreatedAt: m.now().Format(time.RFC3339Nano),
	}
	m.keys[rec.ID] = rec
	if err := m.saveLocked(); err != nil {
		delete(m.keys, rec.ID)
		return Key{}, err
	}
	return *rec, nil
}

// generate 生成 sk- + 24 位随机字符(拒绝采样避免取模偏差)。
func (m *Manager) generate() (string, error) {
	var sb strings.Builder
	sb.WriteString(keyPrefix)
	buf := make([]byte, 1)
	for i := 0; i < autoKeyBodyLen; i++ {
		// 256 % 62 != 0: 取值 >= 248 时重采, 保证均匀分布。
		for {
			if err := m.random(buf); err != nil {
				return "", fmt.Errorf("生成随机秘钥失败: %w", err)
			}
			if buf[0] < 248 {
				break
			}
		}
		sb.WriteByte(autoKeyChars[int(buf[0])%len(autoKeyChars)])
	}
	return sb.String(), nil
}

// randomHex 生成 n 字节随机数的十六进制串(ID 用)。
func randomHex(m *Manager, n int) string {
	buf := make([]byte, n)
	if err := m.random(buf); err != nil {
		// 随机源失败时退化为时间戳(仅 ID 用途, 不涉密)
		return fmt.Sprintf("%x", m.now().UnixNano())[:n*2]
	}
	const hexDigits = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, b := range buf {
		out[i*2] = hexDigits[b>>4]
		out[i*2+1] = hexDigits[b&0x0f]
	}
	return string(out)
}

// validateCustomKey 校验手动秘钥: 长度 8-128、可打印 ASCII 无空白。
func validateCustomKey(key string) error {
	if len(key) < customKeyMinLen || len(key) > customKeyMaxLen {
		return fmt.Errorf("秘钥长度需为 %d-%d 个字符", customKeyMinLen, customKeyMaxLen)
	}
	for _, r := range key {
		if r <= 0x20 || r >= 0x7f {
			return errors.New("秘钥只能包含可打印 ASCII 字符(不含空格)")
		}
	}
	return nil
}

// normalizePermissions 去重并保持输入顺序; 空列表合法(无任何权限)。
func normalizePermissions(perms []string) []string {
	out := make([]string, 0, len(perms))
	seen := map[string]bool{}
	for _, p := range perms {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// normalizeAccountIDs 去重并保持输入顺序; 空列表合法(不限账号)。
// 结果始终为 nil 或非 nil 切片: 空输入返回 nil(序列化为 omitempty 省略)。
func normalizeAccountIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// List 返回全部秘钥, 按创建时间新→旧。
func (m *Manager) List() []Key {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Key, 0, len(m.keys))
	for _, k := range m.keys {
		out = append(out, *k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Get 按 ID 返回秘钥。
func (m *Manager) Get(id string) (Key, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[id]
	if !ok {
		return Key{}, false
	}
	return *k, true
}

// Update 更新名称/权限/启用状态; 秘钥值不可变。
func (m *Manager) Update(id string, in UpdateInput) (Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[id]
	if !ok {
		return Key{}, ErrKeyNotFound
	}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if len([]rune(name)) > nameMaxLen {
			return Key{}, fmt.Errorf("名称最长 %d 字符", nameMaxLen)
		}
		k.Name = name
	}
	if in.Permissions != nil {
		k.Permissions = normalizePermissions(*in.Permissions)
	}
	if in.AccountIDs != nil {
		k.AccountIDs = normalizeAccountIDs(*in.AccountIDs)
	}
	if in.Enabled != nil {
		k.Enabled = *in.Enabled
	}
	if err := m.saveLocked(); err != nil {
		return Key{}, err
	}
	return *k, nil
}

// Delete 删除秘钥。
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.keys[id]; !ok {
		return ErrKeyNotFound
	}
	delete(m.keys, id)
	return m.saveLocked()
}

// Validate 校验秘钥: 命中且启用时返回记录并记录 last_used(节流写盘)。
// 未知/停用/空串一律返回 false(对外不区分, 不泄露秘钥是否存在)。
//
// 恒定时间比较: 用手动秘钥时值可能低熵(8 位), 用 crypto/subtle 常量时间
// 比较并扫完全表(不因匹配/不匹配提前中断), 减少时序侧信道。
func (m *Manager) Validate(key, ip string) (Key, bool) {
	if key == "" {
		return Key{}, false
	}
	keyBytes := []byte(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	var matched *Key
	for _, k := range m.keys {
		// 常量时间比较: 长度不等时 ConstantTimeCompare 返回 0, 不泄露前缀信息。
		if subtle.ConstantTimeCompare([]byte(k.Key), keyBytes) == 1 {
			matched = k
		}
	}
	if matched == nil || !matched.Enabled {
		return Key{}, false
	}
	matched.LastUsedAt = m.now().Format(time.RFC3339)
	matched.LastUsedIP = ip
	if lastUsedPersistInterval <= 0 || m.now().Sub(m.lastPersist) >= lastUsedPersistInterval {
		if err := m.saveLocked(); err == nil {
			m.lastPersist = m.now()
		}
	}
	return *matched, true
}
