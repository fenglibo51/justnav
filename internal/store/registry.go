// 多用户数据隔离：每个账号一个独立的 Store（各自一个 nav.json）。
//
// 为什么按用户分文件而不是在一个 nav.json 里加 userID 字段：
//   - 隔离彻底，任何一处忘了加 where userID 都不会串号
//   - 备份/迁移/删号就是拷/删一个目录
//   - 单用户的文件保持小而完整，出问题容易看
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LocalUserID 是关闭登录（JUSTNAV_AUTH=false）时使用的「本机」用户。
//
// 为什么需要它：关掉登录以后没人往请求里注入用户，而所有业务接口都要按
// 用户找数据，结果每个接口都 401，整个页面用不了。给一个固定身份，
// 数据落在 data/nav.json（单用户时代的位置），升级/降级都不用搬数据。
const LocalUserID = "__local__"

type Registry struct {
	dataRoot string
	root     string
	mu       sync.Mutex
	m        map[string]*Store
}

func NewRegistry(dataRoot string) *Registry {
	return &Registry{
		dataRoot: dataRoot,
		root:     filepath.Join(dataRoot, "users"),
		m:        map[string]*Store{},
	}
}

// Dir 返回某用户的数据目录。本机用户直接用 data 根目录。
func (r *Registry) Dir(userID string) string {
	if userID == LocalUserID {
		return r.dataRoot
	}
	return filepath.Join(r.root, userID)
}

// For 返回用户专属的 Store（惰性加载并缓存）。
func (r *Registry) For(userID string) (*Store, error) {
	if userID == "" {
		return nil, fmt.Errorf("缺少用户标识")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.m[userID]; ok {
		return st, nil
	}
	dir := r.Dir(userID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	st := New(dir)
	if err := st.Load(); err != nil {
		return nil, err
	}
	r.m[userID] = st
	return st, nil
}

// Drop 从缓存里摘掉（删号时用）。
func (r *Registry) Drop(userID string) {
	r.mu.Lock()
	delete(r.m, userID)
	r.mu.Unlock()
}

// RemoveData 删掉某用户的整个数据目录（不可逆）。
func (r *Registry) RemoveData(userID string) error {
	r.Drop(userID)
	return os.RemoveAll(r.Dir(userID))
}

// MigrateLegacy 把 0.1.7 之前的单用户 data/nav.json 归到指定用户目录。
// 只在目标文件不存在时迁移，返回是否真的迁移了。
func (r *Registry) MigrateLegacy(dataRoot, adminID string) (bool, error) {
	if adminID == "" {
		return false, nil
	}
	src := filepath.Join(dataRoot, "nav.json")
	dst := filepath.Join(r.Dir(adminID), "nav.json")

	if _, err := os.Stat(src); err != nil {
		return false, nil // 没有旧数据
	}
	if _, err := os.Stat(dst); err == nil {
		return false, nil // 已经迁移过
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return false, err
	}
	// 旧文件改名留档，避免下次重复迁移
	_ = os.Rename(src, src+".migrated")
	return true, nil
}

// MigrateLegacyWallpaper 把旧的单用户自定义壁纸归到管理员目录。
func (r *Registry) MigrateLegacyWallpaper(dataRoot, adminID string) (bool, error) {
	src := filepath.Join(dataRoot, "wallpaper", "custom.jpg")
	dst := filepath.Join(r.Dir(adminID), "wallpaper.jpg")
	if _, err := os.Stat(src); err != nil {
		return false, nil
	}
	if _, err := os.Stat(dst); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return false, err
	}
	_ = os.Remove(src)
	return true, nil
}
