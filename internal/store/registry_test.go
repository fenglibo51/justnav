package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryIsolatesUsers(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(root)

	a, err := r.For("u-aaa")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.For("u-bbb")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("不同用户不该拿到同一个 Store")
	}

	if err := a.Upsert(Item{Name: "A的服务", Internal: "http://a:1"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Upsert(Item{Name: "B的服务", Internal: "http://b:1"}); err != nil {
		t.Fatal(err)
	}

	// A 只看到自己的
	if items := a.List(); len(items) != 1 || items[0].Name != "A的服务" {
		t.Errorf("A 的数据串了: %+v", items)
	}
	if items := b.List(); len(items) != 1 || items[0].Name != "B的服务" {
		t.Errorf("B 的数据串了: %+v", items)
	}

	// 落盘是两个独立文件
	for _, id := range []string{"u-aaa", "u-bbb"} {
		p := filepath.Join(root, "users", id, "nav.json")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s 的数据文件不存在", id)
		}
	}
}

// 删号要连数据一起删干净
func TestRegistryRemoveData(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(root)
	st, _ := r.For("u-x")
	_ = st.Upsert(Item{Name: "X", Internal: "http://x"})

	if err := r.RemoveData("u-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "users", "u-x")); !os.IsNotExist(err) {
		t.Error("数据目录应被删除")
	}
	// 删完再取会拿到一个空库，不是报错
	st2, err := r.For("u-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.List()) != 0 {
		t.Error("删号后应是空库")
	}
}

// 单用户时代的 data/nav.json 应当迁移到管理员名下，且只迁一次
func TestMigrateLegacyData(t *testing.T) {
	root := t.TempDir()
	legacy := Data{
		Version: schemaVersion,
		Groups:  []string{"工作"},
		Items:   []Item{{ID: "old", Name: "老条目", Group: "工作", Internal: "http://old"}},
	}
	raw := mustJSON(t, legacy)
	if err := os.WriteFile(filepath.Join(root, "nav.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(root)
	moved, err := r.MigrateLegacy(root, "u-admin")
	if err != nil {
		t.Fatal(err)
	}
	if !moved {
		t.Fatal("应该迁移一次")
	}

	st, err := r.For("u-admin")
	if err != nil {
		t.Fatal(err)
	}
	items := st.List()
	if len(items) != 1 || items[0].Name != "老条目" {
		t.Errorf("迁移后条目不对: %+v", items)
	}

	// 旧文件已改名留档，再跑一次不会重复迁移
	moved2, err := r.MigrateLegacy(root, "u-admin")
	if err != nil {
		t.Fatal(err)
	}
	if moved2 {
		t.Error("不该重复迁移")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
