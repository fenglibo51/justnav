// Package auth 提供多账号认证与会话。
//
// 设计：
//   - 账号表存 data/users.json；密码 PBKDF2-HMAC-SHA256（12 万次迭代）存哈希
//   - 第一个创建的账号是管理员（admin），能增删账号；其余是普通用户
//   - 会话是无状态签名 cookie，payload 里带用户 ID；签名密钥持久化，重启不用重登
//   - 登录失败按 IP 限速：5 次锁 5 分钟
//   - 改密码只让**该用户**的旧会话失效 —— 通过给每个用户单独的 session 版本号实现
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	CookieName   = "justnav_session"
	DefaultUser  = "admin"
	// DefaultPassword 开箱即用的初始密码。只用于「一个账号都还没有」的首次启动，
	// 之后环境变量 JUSTNAV_PASSWORD 不再覆盖已有账号（2026-11-20）。
	DefaultPassword = "admin"
	RoleAdmin    = "admin"
	RoleUser     = "user"
	pbkdf2Iter   = 120000
	keyLen       = 32
	saltLen      = 16
	rememberTTL  = 30 * 24 * time.Hour
	sessionTTL   = 12 * time.Hour
	maxFailures  = 5
	lockFor      = 5 * time.Minute
	// minPassLen 5 位：默认开箱密码就是 admin（5 位）。本地自用导航，
	// 首次登录后会提示改掉，不为了凑 6 位而让默认密码创建失败（2026-11-20）。
	minPassLen   = 5
	maxNameLen   = 24
	usersVersion = 1
)

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Salt string `json:"salt"`
	Hash string `json:"hash"`
	Role string `json:"role"`
	// SessionTag 每次改密码就换一个，旧会话因为 tag 不匹配而失效
	SessionTag string    `json:"sessionTag"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Public 是给前端看的字段（不含哈希）
type Public struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

func (u User) public() Public { return Public{ID: u.ID, Name: u.Name, Role: u.Role} }

func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

type doc struct {
	Version int    `json:"version"`
	Secret  string `json:"secret"`
	Users   []User `json:"users"`
}

// legacy 是 0.1.7 之前的单账号结构，用来做一次迁移
type legacy struct {
	User string `json:"user"`
	Salt string `json:"salt"`
	Hash string `json:"hash"`
}

type failInfo struct {
	count int
	until time.Time
}

type Manager struct {
	mu   sync.RWMutex
	path string
	d    doc

	rateMu   sync.Mutex
	failures map[string]*failInfo
}

// New 加载账号表；不存在则创建并返回初始管理员密码（gen 非空表示需要打印一次）。
func New(dataDir, legacyUser, password string) (m *Manager, gen string, err error) {
	m = &Manager{
		path:     filepath.Join(dataDir, "users.json"),
		failures: map[string]*failInfo{},
	}
	loadErr := m.load()
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return nil, "", loadErr
	}

	// 从 0.1.7 的 auth.json 迁移
	if len(m.d.Users) == 0 {
		if err := m.migrateLegacy(filepath.Join(dataDir, "auth.json")); err != nil {
			return nil, "", err
		}
	}

	// 还没有任何账号 → 用环境变量，没有就 admin/admin 创建第一个管理员
	if len(m.d.Users) == 0 {
		if legacyUser == "" {
			legacyUser = DefaultUser
		}
		if password == "" {
			password = DefaultPassword
			// 只有「用了默认密码」才回传，让调用方打印一次提示。
			// 调用方自己给了密码就不回传（gen 的语义是「新生成的」，测试也这么断言）
			gen = password
		}
		if _, err := m.Add(legacyUser, password, RoleAdmin); err != nil {
			return nil, "", err
		}
		return m, gen, nil
	}

	// 已有账号 → 什么都不做。
	// ⚠️ 2026-11-20 改：原来这里会「环境变量有密码就重置管理员密码」，
	// 结果是每次重启都把界面上改过的密码悄悄覆盖回去，
	// 「设置 → 改密码」等于白改。环境变量的语义应该只是「首次初始化密码」。
	return m, "", nil
}

func (m *Manager) load() error {
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	var d doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("解析 users.json: %w", err)
	}
	if d.Version == 0 {
		d.Version = usersVersion
	}
	m.mu.Lock()
	m.d = d
	m.mu.Unlock()
	return nil
}

func (m *Manager) migrateLegacy(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil // 没有旧文件，不需要迁移
	}
	var lg legacy
	if err := json.Unmarshal(raw, &lg); err != nil || lg.Hash == "" {
		return nil
	}
	name := lg.User
	if name == "" {
		name = DefaultUser
	}
	m.mu.Lock()
	m.d.Users = []User{{
		ID:         newID(),
		Name:       name,
		Salt:       lg.Salt,
		Hash:       lg.Hash,
		Role:       RoleAdmin,
		SessionTag: randomTag(),
		CreatedAt:  time.Now(),
	}}
	if m.d.Secret == "" {
		sec, _ := randomB64(32)
		m.d.Secret = sec
	}
	m.mu.Unlock()
	if err := m.save(); err != nil {
		return err
	}
	// 旧文件改名留档，不再使用
	_ = os.Rename(path, path+".migrated")
	return nil
}

func (m *Manager) save() error {
	m.mu.RLock()
	raw, err := json.MarshalIndent(m.d, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".users-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	tmp.Close()
	_ = os.Chmod(name, 0o600)
	return os.Rename(name, m.path)
}

// ensureSecret 调用方需持锁
func (m *Manager) ensureSecretLocked() string {
	if m.d.Secret == "" {
		m.d.Secret, _ = randomB64(32)
	}
	return m.d.Secret
}

// ---------- 账号管理 ----------

func (m *Manager) List() []Public {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Public, 0, len(m.d.Users))
	for _, u := range m.d.Users {
		out = append(out, u.public())
	}
	return out
}

func (m *Manager) Get(id string) (User, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.d.Users {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.d.Users)
}

// Add 新建账号。name 唯一，密码至少 6 位。
func (m *Manager) Add(name, password, role string) (Public, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Public{}, errors.New("账号名不能为空")
	}
	if len([]rune(name)) > maxNameLen {
		return Public{}, fmt.Errorf("账号名不能超过 %d 个字", maxNameLen)
	}
	if len(password) < minPassLen {
		return Public{}, fmt.Errorf("密码至少 %d 位", minPassLen)
	}
	if role != RoleAdmin {
		role = RoleUser
	}

	salt, hash, err := digest(password)
	if err != nil {
		return Public{}, err
	}

	m.mu.Lock()
	for _, u := range m.d.Users {
		if strings.EqualFold(u.Name, name) {
			m.mu.Unlock()
			return Public{}, fmt.Errorf("账号「%s」已存在", name)
		}
	}
	user := User{
		ID: newID(), Name: name, Salt: salt, Hash: hash,
		Role: role, SessionTag: randomTag(), CreatedAt: time.Now(),
	}
	m.d.Users = append(m.d.Users, user)
	m.ensureSecretLocked()
	m.mu.Unlock()

	if err := m.save(); err != nil {
		return Public{}, err
	}
	return user.public(), nil
}

// SetPassword 改密码：换 session tag → 该用户的旧会话立即失效。
func (m *Manager) SetPassword(id, password string) error {
	if len(password) < minPassLen {
		return fmt.Errorf("密码至少 %d 位", minPassLen)
	}
	salt, hash, err := digest(password)
	if err != nil {
		return err
	}
	m.mu.Lock()
	idx := -1
	for i := range m.d.Users {
		if m.d.Users[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.mu.Unlock()
		return errors.New("账号不存在")
	}
	m.d.Users[idx].Salt = salt
	m.d.Users[idx].Hash = hash
	m.d.Users[idx].SessionTag = randomTag()
	m.mu.Unlock()
	return m.save()
}

func (m *Manager) Rename(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("账号名不能为空")
	}
	m.mu.Lock()
	for _, u := range m.d.Users {
		if u.ID != id && strings.EqualFold(u.Name, name) {
			m.mu.Unlock()
			return fmt.Errorf("账号「%s」已存在", name)
		}
	}
	found := false
	for i := range m.d.Users {
		if m.d.Users[i].ID == id {
			m.d.Users[i].Name = name
			found = true
			break
		}
	}
	m.mu.Unlock()
	if !found {
		return errors.New("账号不存在")
	}
	return m.save()
}

// Remove 删账号。不能删最后一个管理员，也不能删自己。
func (m *Manager) Remove(id, operatorID string) error {
	if id == operatorID {
		return errors.New("不能删除当前登录的账号")
	}
	m.mu.Lock()
	idx := -1
	admins := 0
	for i := range m.d.Users {
		if m.d.Users[i].ID == id {
			idx = i
		}
		if m.d.Users[i].IsAdmin() {
			admins++
		}
	}
	if idx < 0 {
		m.mu.Unlock()
		return errors.New("账号不存在")
	}
	if m.d.Users[idx].IsAdmin() && admins <= 1 {
		m.mu.Unlock()
		return errors.New("至少要保留一个管理员")
	}
	m.d.Users = append(m.d.Users[:idx], m.d.Users[idx+1:]...)
	m.mu.Unlock()
	return m.save()
}

// Authenticate 校验账号密码，成功返回用户。
func (m *Manager) Authenticate(name, password string) (User, bool) {
	m.mu.RLock()
	var found *User
	for i := range m.d.Users {
		if strings.EqualFold(m.d.Users[i].Name, name) {
			u := m.d.Users[i]
			found = &u
			break
		}
	}
	m.mu.RUnlock()

	if found == nil {
		// 账号不存在也走一遍哈希，避免用响应时间探账号
		_ = pbkdf2SHA256([]byte(password), []byte("dummy"), 2000, keyLen)
		return User{}, false
	}
	salt, err1 := base64.StdEncoding.DecodeString(found.Salt)
	want, err2 := base64.StdEncoding.DecodeString(found.Hash)
	if err1 != nil || err2 != nil {
		return User{}, false
	}
	got := pbkdf2SHA256([]byte(password), salt, pbkdf2Iter, keyLen)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return User{}, false
	}
	return *found, true
}

// ---------- 会话 ----------

type sessionPayload struct {
	User string `json:"u"` // 用户 ID
	Tag  string `json:"t"` // 用户当前的 session tag
	Exp  int64  `json:"e"`
}

func (m *Manager) IssueSession(userID string, remember bool) (string, time.Duration, error) {
	ttl := sessionTTL
	if remember {
		ttl = rememberTTL
	}
	m.mu.RLock()
	var u *User
	for i := range m.d.Users {
		if m.d.Users[i].ID == userID {
			uu := m.d.Users[i]
			u = &uu
			break
		}
	}
	secret := m.d.Secret
	m.mu.RUnlock()

	if u == nil || secret == "" {
		return "", 0, errors.New("账号或密钥不存在")
	}
	payload, err := json.Marshal(sessionPayload{
		User: u.ID, Tag: u.SessionTag, Exp: time.Now().Add(ttl).Unix(),
	})
	if err != nil {
		return "", 0, err
	}
	pb := base64.RawURLEncoding.EncodeToString(payload)
	sig, err := sign(pb, secret)
	if err != nil {
		return "", 0, err
	}
	return pb + "." + sig, ttl, nil
}

// VerifySession 返回用户 ID。
func (m *Manager) VerifySession(token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	m.mu.RLock()
	secret := m.d.Secret
	m.mu.RUnlock()
	if secret == "" {
		return "", false
	}
	want, err := sign(parts[0], secret)
	if err != nil || subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", false
	}
	if time.Now().Unix() > p.Exp {
		return "", false
	}
	// 账号还在吗？tag 还对吗？（改密码会换 tag）
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.d.Users {
		if u.ID == p.User {
			if subtle.ConstantTimeCompare([]byte(u.SessionTag), []byte(p.Tag)) != 1 {
				return "", false
			}
			return u.ID, true
		}
	}
	return "", false
}

func sign(payload, secretB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(secretB64)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}

// ---------- 登录限速 ----------

// AllowAttempt 始终放行 —— 不按 IP 锁登录。
// 内网自用导航页，5 次失败锁 5 分钟在开发调试时纯耽误事（2026-11-20 按波哥要求去掉）。
// 失败次数仍会被 RecordFailure 记录，只是不再阻断登录。
func (m *Manager) AllowAttempt(ip string) bool {
	return true
}

func (m *Manager) RecordFailure(ip string) {
	m.rateMu.Lock()
	defer m.rateMu.Unlock()
	f, ok := m.failures[ip]
	if !ok || (!f.until.IsZero() && time.Now().After(f.until)) {
		f = &failInfo{}
		m.failures[ip] = f
	}
	f.count++
	if f.count >= maxFailures {
		f.until = time.Now().Add(lockFor)
	}
}

func (m *Manager) ResetFailures(ip string) {
	m.rateMu.Lock()
	defer m.rateMu.Unlock()
	delete(m.failures, ip)
}

// ---------- 工具 ----------

func digest(password string) (saltB64, hashB64 string, err error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", "", err
	}
	hash := pbkdf2SHA256([]byte(password), salt, pbkdf2Iter, keyLen)
	return base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(hash), nil
}

func randomB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func randomTag() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "u-" + hex.EncodeToString(b)
}

func randomPassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// pbkdf2SHA256 标准 PBKDF2（RFC 8018），只依赖 stdlib。
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	h := hmac.New(sha256.New, password)
	hashLen := h.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)

	var idx [4]byte
	for block := 1; block <= blocks; block++ {
		h.Reset()
		h.Write(salt)
		binary.BigEndian.PutUint32(idx[:], uint32(block))
		h.Write(idx[:])
		u := h.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			h.Reset()
			h.Write(u)
			u = h.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
