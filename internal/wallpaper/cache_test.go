package wallpaper

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFake(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func day(offset int) string {
	return time.Now().AddDate(0, 0, offset).Format("20060102")
}

func TestCleanupRemovesExpired(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, t.TempDir())
	writeFake(t, dir, day(0)+".jpg", 100)
	writeFake(t, dir, day(-5)+".jpg", 100)
	writeFake(t, dir, day(-40)+".jpg", 100)
	writeFake(t, dir, "readme.txt", 10) // 非壁纸文件不该被碰

	n, err := m.Cleanup(30, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("应删 1 个过期文件, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, day(-40)+".jpg")); !os.IsNotExist(err) {
		t.Error("40 天前的文件应被删除")
	}
	if _, err := os.Stat(filepath.Join(dir, day(-5)+".jpg")); err != nil {
		t.Error("5 天前的文件应保留")
	}
	if _, err := os.Stat(filepath.Join(dir, "readme.txt")); err != nil {
		t.Error("非壁纸文件不该被删")
	}
}

func TestCleanupRespectsMaxBytes(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, t.TempDir())
	// 5 个文件 × 100 字节，上限 250 → 只能留 2 个
	for i := 0; i < 5; i++ {
		writeFake(t, dir, day(-i)+".jpg", 100)
	}
	n, err := m.Cleanup(365, 250)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("容量超限应删 3 个, got %d", n)
	}
	var left int
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if dateRe.MatchString(e.Name()) {
			left++
		}
	}
	if left != 2 {
		t.Errorf("应剩 2 个, got %d", left)
	}
	// 必须保留最新的那个
	if _, err := os.Stat(filepath.Join(dir, day(0)+".jpg")); err != nil {
		t.Error("最新的壁纸被误删了")
	}
}

func TestCleanupNeverDeletesCurrent(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, t.TempDir())
	old := day(-100)
	writeFake(t, dir, old+".jpg", 1000)
	// 手动把 current 指到这个很老的文件
	m.mu.Lock()
	m.cur = &Meta{Date: old, File: old + ".jpg"}
	m.mu.Unlock()

	n, err := m.Cleanup(30, 1) // 天数和容量都该杀掉它
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("当前使用中的壁纸不该被删, removed=%d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, old+".jpg")); err != nil {
		t.Error("当前壁纸被误删了")
	}
}

func TestCachedSortedNewestFirst(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, t.TempDir())
	writeFake(t, dir, day(-2)+".jpg", 10)
	writeFake(t, dir, day(0)+".jpg", 10)
	writeFake(t, dir, day(-1)+".jpg", 10)

	list := m.Cached()
	if len(list) != 3 {
		t.Fatalf("应 3 个, got %d", len(list))
	}
	if list[0].Date != day(0) {
		t.Errorf("首个应是最新, got %s", list[0].Date)
	}
}

func TestLoadCurrentMissingIsFine(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir())
	if err := m.LoadCurrent(); err != nil {
		t.Errorf("没有 meta 不该报错: %v", err)
	}
	if _, ok := m.Current(); ok {
		t.Error("不该有 current")
	}
}
