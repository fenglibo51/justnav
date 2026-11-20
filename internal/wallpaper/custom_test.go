package wallpaper

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func makePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 120, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestSaveCustomAcceptsPNG(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir())
	data := makePNG(800, 600)

	w, h, err := m.SaveCustom("u1", data)
	if err != nil {
		t.Fatalf("应接受 png: %v", err)
	}
	if w != 800 || h != 600 {
		t.Errorf("尺寸应为 800x600, got %dx%d", w, h)
	}
	if !m.HasCustom("u1") {
		t.Error("落盘后 HasCustom 应为 true")
	}
	// 应被重编码成 JPEG
	st, err := os.Stat(m.CustomPath("u1"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() == 0 {
		t.Error("文件为空")
	}
	raw, _ := os.ReadFile(m.CustomPath("u1"))
	if _, format, err := image.DecodeConfig(bytes.NewReader(raw)); err != nil || format != "jpeg" {
		t.Errorf("应重编码为 jpeg, got %s err=%v", format, err)
	}
}

func TestSaveCustomRejectsNonImage(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir())
	if _, _, err := m.SaveCustom("u1", []byte("<html>not an image</html>")); err == nil {
		t.Error("HTML 应被拒绝")
	}
	if m.HasCustom("u1") {
		t.Error("失败时不该产生文件")
	}
}

func TestSaveCustomRejectsHugePixels(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir())
	// 只需要 header 里声明大尺寸，不需要真的大图
	img := image.NewRGBA(image.Rect(0, 0, 6000, 5000)) // 3000 万像素 > 2400 万
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 30})
	if _, _, err := m.SaveCustom("u1", buf.Bytes()); err == nil {
		t.Error("超过像素上限应被拒绝")
	}
}

func TestSaveCustomReplacesAtomically(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir())
	if _, _, err := m.SaveCustom("u1", makePNG(100, 100)); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(m.CustomPath("u1"))

	if _, _, err := m.SaveCustom("u1", makePNG(200, 200)); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(m.CustomPath("u1"))
	if bytes.Equal(first, second) {
		t.Error("第二次上传应覆盖")
	}
	// 目录里不该有临时文件残留
	entries, _ := os.ReadDir(filepath.Dir(m.CustomPath("u1")))
	for _, e := range entries {
		if e.Name() != customFile && e.Name() != metaFile {
			t.Errorf("残留文件: %s", e.Name())
		}
	}
}

// 自定义图不能被日期清理删掉
func TestCleanupKeepsCustom(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, t.TempDir())
	if _, _, err := m.SaveCustom("u1", makePNG(50, 50)); err != nil {
		t.Fatal(err)
	}
	writeFake(t, dir, day(-90)+".jpg", 100)

	if _, err := m.Cleanup(30, 1); err != nil {
		t.Fatal(err)
	}
	if !m.HasCustom("u1") {
		t.Error("自定义壁纸被清理掉了")
	}
}

// 两个用户的自定义壁纸必须互不影响
func TestCustomWallpaperIsolatedPerUser(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir())

	if _, _, err := m.SaveCustom("u1", makePNG(120, 90)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.SaveCustom("u2", makePNG(60, 40)); err != nil {
		t.Fatal(err)
	}
	if m.CustomPath("u1") == m.CustomPath("u2") {
		t.Fatal("两个用户不该共用同一个文件")
	}
	if !m.HasCustom("u1") || !m.HasCustom("u2") {
		t.Error("两个人的壁纸都该存在")
	}
	r1, _ := os.ReadFile(m.CustomPath("u1"))
	r2, _ := os.ReadFile(m.CustomPath("u2"))
	if bytes.Equal(r1, r2) {
		t.Error("两个用户的壁纸内容不该相同（说明写串了）")
	}
	if m.HasCustom("u3") {
		t.Error("没上传过的用户不该有壁纸")
	}
}
