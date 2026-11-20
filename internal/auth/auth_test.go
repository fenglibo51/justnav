package auth

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func newMgr(t *testing.T) *Manager {
	t.Helper()
	m, gen, err := New(t.TempDir(), "admin", "admin123456")
	if err != nil {
		t.Fatal(err)
	}
	if gen != "" {
		t.Errorf("给了密码就不该再生成: %q", gen)
	}
	return m
}

func TestBootstrapAdmin(t *testing.T) {
	m := newMgr(t)
	users := m.List()
	if len(users) != 1 {
		t.Fatalf("应只有 1 个账号, got %d", len(users))
	}
	if users[0].Role != RoleAdmin {
		t.Errorf("第一个账号应为管理员, got %q", users[0].Role)
	}
	if _, ok := m.Authenticate("admin", "admin123456"); !ok {
		t.Error("正确密码应通过")
	}
	if _, ok := m.Authenticate("admin", "wrong"); ok {
		t.Error("错误密码不该通过")
	}
	if _, ok := m.Authenticate("nobody", "admin123456"); ok {
		t.Error("不存在的账号不该通过")
	}
}

func TestPasswordNotStoredPlaintext(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := New(dir, "admin", "plaintext-secret"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dir + "/users.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "plaintext-secret") {
		t.Fatal("users.json 里出现了明文密码")
	}
}

func TestAddUser(t *testing.T) {
	m := newMgr(t)

	u, err := m.Add("wife", "wife123456", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "wife" || u.Role != RoleUser {
		t.Errorf("新建账号字段不对: %+v", u)
	}
	if _, err := m.Add("wife", "another1", RoleUser); err == nil {
		t.Error("重名应该被拒绝")
	}
	if _, err := m.Add("WIFE", "another1", RoleUser); err == nil {
		t.Error("大小写不同的重名也应该被拒绝")
	}
	if _, err := m.Add("x", "123", RoleUser); err == nil {
		t.Error("过短密码应该被拒绝")
	}
	if _, err := m.Add("", "12345678", RoleUser); err == nil {
		t.Error("空账号名应该被拒绝")
	}

	if _, ok := m.Authenticate("wife", "wife123456"); !ok {
		t.Error("新账号应能登录")
	}
	if len(m.List()) != 2 {
		t.Errorf("应有 2 个账号, got %d", len(m.List()))
	}
}

// 每个账号有不同的数据命名空间（这里只验 ID 唯一）
func TestUsersHaveUniqueIDs(t *testing.T) {
	m := newMgr(t)
	a, _ := m.Add("a", "aaaaaa1", RoleUser)
	b, _ := m.Add("b", "bbbbbb1", RoleUser)
	if a.ID == b.ID || a.ID == "" || b.ID == "" {
		t.Errorf("账号 ID 必须唯一且非空: %q %q", a.ID, b.ID)
	}
}

// 改密码只让该用户的旧会话失效，不影响别人
func TestPasswordChangeInvalidatesOnlyThatUser(t *testing.T) {
	m := newMgr(t)
	wife, err := m.Add("wife", "wife123456", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	adminID := m.List()[0].ID

	adminTok, _, err := m.IssueSession(adminID, true)
	if err != nil {
		t.Fatal(err)
	}
	wifeTok, _, err := m.IssueSession(wife.ID, true)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.SetPassword(wife.ID, "newpass123"); err != nil {
		t.Fatal(err)
	}

	if _, ok := m.VerifySession(adminTok); !ok {
		t.Error("管理员会话不该受影响")
	}
	if _, ok := m.VerifySession(wifeTok); ok {
		t.Error("改密码的账号，旧会话必须失效")
	}
	if _, ok := m.Authenticate("wife", "newpass123"); !ok {
		t.Error("新密码应生效")
	}
	if _, ok := m.Authenticate("wife", "wife123456"); ok {
		t.Error("旧密码应失效")
	}
}

func TestRemoveUserRules(t *testing.T) {
	m := newMgr(t)
	adminID := m.List()[0].ID
	wife, _ := m.Add("wife", "wife123456", RoleUser)

	if err := m.Remove(adminID, adminID); err == nil {
		t.Error("不能删自己")
	}
	if err := m.Remove(wife.ID, adminID); err != nil {
		t.Errorf("删普通用户应成功: %v", err)
	}
	if len(m.List()) != 1 {
		t.Error("删完应只剩管理员")
	}

	// 只剩一个管理员时不能删
	m2, _, _ := New(t.TempDir(), "admin", "admin123456")
	only := m2.List()[0].ID
	m2.Add("temp", "temp12345", RoleAdmin)
	if err := m2.Remove(only, "someone-else"); err != nil {
		t.Errorf("还有另一个管理员时应该能删: %v", err)
	}
	if err := m2.Remove(m2.List()[0].ID, "x"); err == nil {
		t.Error("最后一个管理员不能删")
	}
}

func TestSessionIssueAndVerify(t *testing.T) {
	m := newMgr(t)
	uid := m.List()[0].ID

	token, ttl, err := m.IssueSession(uid, true)
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 {
		t.Error("ttl 应为正")
	}
	got, ok := m.VerifySession(token)
	if !ok || got != uid {
		t.Errorf("正常 token 应通过, ok=%v uid=%q", ok, got)
	}

	parts := strings.SplitN(token, ".", 2)
	if _, ok := m.VerifySession(parts[0] + ".AAAA" + parts[1][4:]); ok {
		t.Error("签名被篡改后不该通过")
	}
	if _, ok := m.VerifySession("ZmFrZQ." + parts[1]); ok {
		t.Error("payload 被篡改后不该通过")
	}
	for _, bad := range []string{"", "abc", "a.b.c"} {
		if _, ok := m.VerifySession(bad); ok {
			t.Errorf("%q 不该通过", bad)
		}
	}
}

func TestSessionExpiry(t *testing.T) {
	m := newMgr(t)
	uid := m.List()[0].ID
	u, _ := m.Get(uid)

	payload, _ := json.Marshal(sessionPayload{User: uid, Tag: u.SessionTag, Exp: 1})
	pb := base64.RawURLEncoding.EncodeToString(payload)
	sig, err := sign(pb, m.d.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.VerifySession(pb + "." + sig); ok {
		t.Error("过期会话应失效")
	}
}

// 注：原 TestRateLimit / TestLockExpires 两条用例随「不锁 IP」一起删除
// （AllowAttempt 现在恒返回 true，锁定行为已不存在）。

// 从 0.1.7 的单账号 auth.json 自动迁移
func TestMigrateLegacyAuthFile(t *testing.T) {
	dir := t.TempDir()
	salt, hash, err := digest("oldpass123")
	if err != nil {
		t.Fatal(err)
	}
	lg := map[string]string{"user": "olduser", "salt": salt, "hash": hash}
	raw, _ := json.Marshal(lg)
	if err := os.WriteFile(dir+"/auth.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}

	m, gen, err := New(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if gen != "" {
		t.Error("迁移后不该再生成密码")
	}
	if _, ok := m.Authenticate("olduser", "oldpass123"); !ok {
		t.Error("旧密码迁移后应仍有效")
	}
	users := m.List()
	if len(users) != 1 || users[0].Role != RoleAdmin {
		t.Errorf("迁移出来的应该是管理员: %+v", users)
	}
	if _, err := os.Stat(dir + "/auth.json.migrated"); err != nil {
		t.Error("旧文件应改名留档")
	}
}

func TestRename(t *testing.T) {
	m := newMgr(t)
	wife, _ := m.Add("wife", "wife123456", RoleUser)
	if err := m.Rename(wife.ID, "wife2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Authenticate("wife2", "wife123456"); !ok {
		t.Error("改名后应能用新名字登录")
	}
	if err := m.Rename(wife.ID, "admin"); err == nil {
		t.Error("不能和别人的名字撞")
	}
}

func TestPBKDF2Vector(t *testing.T) {
	got := pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32)
	want := "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	hexGot := ""
	for _, b := range got {
		hexGot += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&0xf])
	}
	if hexGot != want {
		t.Errorf("PBKDF2 向量不匹配\n got=%s\nwant=%s", hexGot, want)
	}
}
