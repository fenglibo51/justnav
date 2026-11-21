package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadMissingCreatesEmptyLibrary(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Load(); err != nil {
		t.Fatalf("Load 应容忍文件不存在: %v", err)
	}
	d := s.Snapshot()
	if d.Version != schemaVersion {
		t.Errorf("version = %d, want %d", d.Version, schemaVersion)
	}
	if d.Settings.NetMode != "in" {
		t.Errorf("默认 netMode 应为 in, got %q", d.Settings.NetMode)
	}
	if len(d.Items) != 0 {
		t.Errorf("默认应无条目, got %d", len(d.Items))
	}
}

func TestUpsertSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Item{Name: "Plex", Group: "影音媒体", Internal: "http://10.0.0.2:32400"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Item{Name: "Plex", Group: "影音媒体", Internal: "http://10.0.0.2:32400", External: "https://plex.example.com"}); err != nil {
		t.Fatal(err)
	}

	// 重新打开验证真的落盘了
	s2 := New(dir)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	items := s2.List()
	if len(items) != 1 {
		t.Fatalf("应为 1 条（同名更新不应重复），got %d", len(items))
	}
	if items[0].External != "https://plex.example.com" {
		t.Errorf("外网地址未持久化: %+v", items[0])
	}
}

func TestSaveIsAtomicAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	_ = s.Load()
	if err := s.Upsert(Item{Name: "A"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.Save()
		}(i)
	}
	wg.Wait()

	// 落盘内容必须是完整合法 JSON
	raw, err := os.ReadFile(filepath.Join(dir, "nav.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d Data
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("落盘内容不是合法 JSON: %v\n%s", err, raw)
	}
	// 不允许残留临时文件
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "nav.json" {
			t.Errorf("残留文件: %s", e.Name())
		}
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	_ = s.Load()
	_ = s.Upsert(Item{Name: "A"})
	_ = s.Upsert(Item{Name: "B"})

	ok, err := s.Delete("a")
	if err != nil || !ok {
		t.Fatalf("删除 a 失败: ok=%v err=%v", ok, err)
	}
	if got := len(s.List()); got != 1 {
		t.Errorf("删除后应剩 1 条, got %d", got)
	}
	ok, err = s.Delete("not-exist")
	if err != nil || ok {
		t.Errorf("删除不存在条目应返回 false/nil: ok=%v err=%v", ok, err)
	}
}

func TestGroupAddRenameDelete(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	_ = s.Load()

	if err := s.AddGroup("测试组"); err != nil {
		t.Fatalf("AddGroup: %v", err)
	}
	if err := s.AddGroup("测试组"); err == nil {
		t.Error("重复添加同名分组应报错")
	}
	if err := s.AddGroup("  "); err == nil {
		t.Error("空白名应被拒绝")
	}

	_ = s.Upsert(Item{Name: "A", Group: "测试组"})
	if err := s.RenameGroup("测试组", "改名后"); err != nil {
		t.Fatalf("RenameGroup: %v", err)
	}
	groups := s.Snapshot().Groups
	if contains(groups, "测试组") {
		t.Error("旧分组名应消失")
	}
	items := s.List()
	if len(items) != 1 || items[0].Group != "改名后" {
		t.Errorf("组内条目应跟着改名, got %+v", items)
	}

	// 组里还有条目，不给迁移目标必须报错且不删
	if _, err := s.DeleteGroup("改名后", ""); err == nil {
		t.Error("非空分组不给 moveTo 应报错")
	}
	if !contains(s.Snapshot().Groups, "改名后") {
		t.Error("报错时不应该真的删掉分组")
	}

	// 给了迁移目标才允许删
	moved, err := s.DeleteGroup("改名后", "未分组")
	if err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if moved != 1 {
		t.Errorf("应迁移 1 个条目, got %d", moved)
	}
	items = s.List()
	if len(items) != 1 || items[0].Group != "未分组" {
		t.Errorf("条目应迁到未分组, got %+v", items)
	}
	if !contains(s.Snapshot().Groups, "未分组") {
		t.Error("迁移目标分组应被自动创建")
	}
}

func TestGroupOrder(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	_ = s.Load()
	// 自带分组，不依赖 defaultData 里的预置值（2026-11-20 起默认分组为空）
	_ = s.AddGroup("A组")
	_ = s.AddGroup("C组")
	_ = s.AddGroup("D组")

	if err := s.SetGroupOrder([]string{"D组", "A组"}); err != nil {
		t.Fatal(err)
	}
	got := s.Snapshot().Groups
	if got[0] != "D组" || got[1] != "A组" {
		t.Errorf("前两位顺序不对: %v", got)
	}
	// 未提及的分组不能丢
	if !contains(got, "C组") {
		t.Errorf("未提及的分组丢失了: %v", got)
	}
}

func TestSettingsPatch(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	_ = s.Load()
	if err := s.SetSettings(Settings{NetMode: "out"}); err != nil {
		t.Fatal(err)
	}
	got := s.Snapshot().Settings
	if got.NetMode != "out" {
		t.Errorf("netMode = %q, want out", got.NetMode)
	}
	if got.Theme != "glass" {
		t.Errorf("未提供的字段不应被清空, theme = %q", got.Theme)
	}
}

// 跨组拖拽 = 同时改顺序和分组，两者必须一起生效
func TestSetLayoutMovesAcrossGroups(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	_ = st.Load()
	_ = st.Upsert(Item{Name: "A", Group: "影音媒体"})
	_ = st.Upsert(Item{Name: "B", Group: "影音媒体"})
	_ = st.Upsert(Item{Name: "C", Group: "下载与存储"})

	// 把 B 拖到「下载与存储」的最前面，A 留在原位
	err := st.SetLayout([]Placement{
		{ID: "b", Group: "下载与存储"},
		{ID: "a", Group: "影音媒体"},
		{ID: "c", Group: "下载与存储"},
	})
	if err != nil {
		t.Fatal(err)
	}

	items := st.List()
	if len(items) != 3 {
		t.Fatalf("条目数不该变, got %d", len(items))
	}
	want := []struct{ id, group string }{
		{"b", "下载与存储"}, {"a", "影音媒体"}, {"c", "下载与存储"},
	}
	for i, w := range want {
		if items[i].ID != w.id || items[i].Group != w.group {
			t.Errorf("第 %d 位应为 %s/%s, got %s/%s", i, w.id, w.group, items[i].ID, items[i].Group)
		}
		if items[i].Order != i {
			t.Errorf("Order 未按位置重排: %d -> %d", i, items[i].Order)
		}
	}
}

// 提交里漏掉的条目不能被丢掉（前端漏传 / 并发编辑都可能）
func TestSetLayoutKeepsUntouchedItems(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	_ = st.Load()
	_ = st.Upsert(Item{Name: "Keep", Group: "影音媒体"})
	_ = st.Upsert(Item{Name: "Move", Group: "影音媒体"})

	if err := st.SetLayout([]Placement{{ID: "move", Group: "开发工具"}}); err != nil {
		t.Fatal(err)
	}
	items := st.List()
	if len(items) != 2 {
		t.Fatalf("漏传的条目被丢了, got %d", len(items))
	}
	var kept *Item
	for i := range items {
		if items[i].ID == "keep" {
			kept = &items[i]
		}
	}
	if kept == nil || kept.Group != "影音媒体" {
		t.Errorf("未提及的条目应保持原分组, got %+v", kept)
	}
}

// 新分组在布局提交时若不存在要自动创建
func TestSetLayoutCreatesMissingGroup(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	_ = st.Load()
	_ = st.Upsert(Item{Name: "X", Group: "影音媒体"})

	if err := st.SetLayout([]Placement{{ID: "x", Group: "新分组"}}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range st.Snapshot().Groups {
		if g == "新分组" {
			found = true
		}
	}
	if !found {
		t.Error("目标分组不存在时应自动创建")
	}
}

func TestBulkUpsert(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	_ = st.Load()
	_ = st.Upsert(Item{Name: "已有", Group: "影音媒体", Internal: "http://a:1"})

	added, updated, err := st.BulkUpsert([]Item{
		{Name: "新一", Group: "导入组", Internal: "http://b:2"},
		{Name: "已有", Group: "影音媒体", Internal: "http://a:1"}, // 同 id 同地址 → 更新
		{Name: "", Internal: "http://c:3"},                  // 缺名字 → 跳过
		{Name: "缺地址"},                                       // 缺地址 → 跳过
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || updated != 1 {
		t.Errorf("added=%d updated=%d, want 1/1", added, updated)
	}
	items := st.List()
	if len(items) != 2 {
		t.Errorf("应共 2 条, got %d", len(items))
	}
	// 新分组自动创建
	found := false
	for _, g := range st.Snapshot().Groups {
		if g == "导入组" {
			found = true
		}
	}
	if !found {
		t.Error("导入的新分组应自动创建")
	}
}

// 同名但地址不同 = 两个不同服务，不能合成一条
func TestBulkUpsertSameNameDifferentURL(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	_ = st.Load()

	added, _, err := st.BulkUpsert([]Item{
		{Name: "Plex", Internal: "http://10.0.0.1:32400"},
		{Name: "Plex", Internal: "http://10.0.0.2:32400"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Errorf("同名不同地址应各存一条, added=%d", added)
	}
	if len(st.List()) != 2 {
		t.Errorf("应共 2 条, got %d", len(st.List()))
	}
}

// 导入不能打乱已有条目的顺序
func TestBulkUpsertKeepsExistingOrder(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	_ = st.Load()
	_ = st.Upsert(Item{Name: "A", Internal: "http://a"})
	_ = st.Upsert(Item{Name: "B", Internal: "http://b"})

	_, _, err := st.BulkUpsert([]Item{{Name: "A", Internal: "http://a", Tag: "改过"}})
	if err != nil {
		t.Fatal(err)
	}
	items := st.List()
	if items[0].ID != "a" || items[1].ID != "b" {
		t.Errorf("原有顺序被打乱: %v", []string{items[0].ID, items[1].ID})
	}
	if items[0].Tag != "改过" {
		t.Errorf("应更新成功, tag=%q", items[0].Tag)
	}
}

// 一次批量导入只应该落盘一次（几百条书签逐条 Save 会很慢）
func TestBulkUpsertSingleSave(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	_ = st.Load()

	many := make([]Item, 0, 200)
	for i := 0; i < 200; i++ {
		many = append(many, Item{Name: "svc-" + string(rune('a'+i%26)) + "-" + itoa(i), Internal: "http://h:" + itoa(i)})
	}
	if _, _, err := st.BulkUpsert(many); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("目录里应只有 nav.json，残留临时文件: %d 个", len(entries))
	}
	if len(st.List()) != 200 {
		t.Errorf("应导入 200 条, got %d", len(st.List()))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// 没有分组的条目必须落到「未分组」，并且要出现在 Groups 列表里。
// 否则前端按 groups 渲染时会「条目存在但看不见」（真实踩过的 bug）。
func TestUpsertWithoutGroupFallsBack(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	_ = st.Load()

	if err := st.Upsert(Item{Name: "komga", Internal: "http://h:25600", Source: "docker"}); err != nil {
		t.Fatal(err)
	}
	items := st.List()
	if len(items) != 1 || items[0].Group != DefaultGroup {
		t.Fatalf("应落到 %q, got %+v", DefaultGroup, items)
	}
	found := false
	for _, g := range st.Snapshot().Groups {
		if g == DefaultGroup {
			found = true
		}
	}
	if !found {
		t.Errorf("%q 必须在 Groups 列表里，否则前端渲染不到。groups=%v", DefaultGroup, st.Snapshot().Groups)
	}
}

// 批量导入同样要兜底
func TestBulkUpsertWithoutGroupFallsBack(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	_ = st.Load()
	added, _, err := st.BulkUpsert([]Item{{Name: "x", Internal: "http://x"}})
	if err != nil || added != 1 {
		t.Fatalf("added=%d err=%v", added, err)
	}
	if items := st.List(); items[0].Group != DefaultGroup {
		t.Errorf("应落到 %q, got %q", DefaultGroup, items[0].Group)
	}
	if !contains(st.Snapshot().Groups, DefaultGroup) {
		t.Error("未分组必须在 Groups 里")
	}
}

// Snapshot 必须把 Settings 里的引用类型也拷出来。
//
// 这条是回归测试：以前的 Snapshot 只 `d := s.data` + 拷 Groups/Items，
// Settings 里的 map / slice 复制走的只是 header，跟内部状态共用同一份
// 底层数据。调用方拿着快照在锁外改它，等于绕过锁改内部状态；反方向则是
// 并发读写 map —— Go 会直接 fatal error 崩进程，不是脏数据。
//
// 没有 gcc 跑不了 -race，所以这里用「改快照不能影响内部」来确定性证明。
func TestSnapshotIsolatesSettingsReferences(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	err := st.SetSettings(Settings{
		GroupIcons:  map[string]string{"媒体": "film"},
		Engines:     []Engine{{ID: "e1", Name: "原始引擎", URL: "https://a/%s"}},
		Widgets:     []string{"calendar"},
		Downloaders: []Downloader{{ID: "d1", Name: "qb", Type: "qb", URL: "http://h:8085", Pass: "secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	snap := st.Snapshot()
	// 往快照里可劲儿改
	snap.Settings.GroupIcons["媒体"] = "被改了"
	snap.Settings.GroupIcons["新增组"] = "x"
	snap.Settings.Engines[0].Name = "被改了"
	snap.Settings.Widgets[0] = "被改了"
	snap.Settings.Downloaders[0].Pass = "被改了"

	again := st.Snapshot()
	if got := again.Settings.GroupIcons["媒体"]; got != "film" {
		t.Errorf("GroupIcons 被快照改到了内部: %q", got)
	}
	if _, ok := again.Settings.GroupIcons["新增组"]; ok {
		t.Error("往快照的 map 里加键，污染了内部状态")
	}
	if got := again.Settings.Engines[0].Name; got != "原始引擎" {
		t.Errorf("Engines 是浅拷贝: %q", got)
	}
	if got := again.Settings.Widgets[0]; got != "calendar" {
		t.Errorf("Widgets 是浅拷贝: %q", got)
	}
	if got := again.Settings.Downloaders[0].Pass; got != "secret" {
		t.Errorf("Downloaders 是浅拷贝: %q", got)
	}
}

// 反过来也要成立：内部改了，之前拿到的快照不能跟着变。
func TestSnapshotUnchangedByLaterWrites(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSettings(Settings{GroupIcons: map[string]string{"A": "one"}}); err != nil {
		t.Fatal(err)
	}
	snap := st.Snapshot()
	if err := st.SetSettings(Settings{GroupIcons: map[string]string{"A": "two", "B": "three"}}); err != nil {
		t.Fatal(err)
	}
	if got := snap.Settings.GroupIcons["A"]; got != "one" {
		t.Errorf("内部改动渗透进了旧快照: %q", got)
	}
	if _, ok := snap.Settings.GroupIcons["B"]; ok {
		t.Error("内部新增的键出现在旧快照里")
	}
}

// 老版本可能把 nav.json 建成 0644，而里面的 Settings.Downloaders[].Pass
// 是明文下载器密码。Load() 只 ReadFile 不 chmod 的话，升级后那个文件
// 一直是 0644 —— 用户什么都不做就永远泄露着。
func TestLoadTightensExistingPermission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nav.json")
	body := []byte(`{"version":1,"groups":["未分组"],"items":[]}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	t.Logf("加载前权限 %v", before.Mode().Perm())

	st := New(dir)
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("加载后权限 %v", after.Mode().Perm())
	if after.Mode().Perm() != 0o600 {
		t.Errorf("加载后应收到 0600，实际 %v —— 旧 nav.json 的下载器密码仍然对全机可读",
			after.Mode().Perm())
	}
}

// Save() 的序列化和落盘在锁外，两个并发 Save 可能反向完成 rename，
// 磁盘最终留下较旧的那份。用「并发 Upsert 后重新加载看有没有丢条目」
// 来探测 —— Upsert 是 read-modify-write + Save，正好是并发 Save 场景。
func TestConcurrentSaveKeepsAllWrites(t *testing.T) {
	const rounds = 8
	const perGoroutine = 10
	const want = perGoroutine * 2
	lost := 0
	for r := 0; r < rounds; r++ {
		dir := t.TempDir()
		st := New(dir)
		if err := st.Load(); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		for _, p := range []string{"a", "b"} {
			go func(prefix string) {
				defer wg.Done()
				for i := 0; i < perGoroutine; i++ {
					_ = st.Upsert(Item{
						Name:     prefix + "-" + string(rune('a'+i)),
						Internal: "http://" + prefix + ".example",
					})
				}
			}(p)
		}
		wg.Wait()

		// 换个 Store 重新从磁盘读，看并发写的都在不在
		fresh := New(dir)
		if err := fresh.Load(); err != nil {
			t.Fatal(err)
		}
		if got := len(fresh.List()); got != want {
			lost++
			if lost <= 3 {
				t.Logf("第 %d 轮：磁盘上只有 %d 条，丢了 %d 条", r, got, want-got)
			}
		}
	}
	if lost > 0 {
		t.Errorf("%d/%d 轮出现并发保存丢写 —— Save() 的落盘顺序没有串行化", lost, rounds)
	}
}
