package favicon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 假站点：首页用 <link rel="icon"> 声明一个带 onload 的 SVG。
// 这正是 /api/favicon 的威胁路径 —— 抓回来的 SVG 会以本站源返回给浏览器。
func evilSite(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 固定路径候选（/favicon.ico 等）都返回 HTML，让 extFor 判成"不是图标"，
		// 逼它走第二轮 discoverIconLinks
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><html><head>`+
			`<link rel="icon" type="image/svg+xml" href="/evil.svg">`+
			`</head><body>x</body></html>`)
	})
	mux.HandleFunc("/evil.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		// 属性名和等号之间有空格 —— 字符串黑名单拦不住，XML 解析器照样认
		io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" onload = "alert(1)"></svg>`)
	})
	return httptest.NewServer(mux)
}

// 抓到可执行 SVG 时必须当"没找到图标"，不能落盘。
func TestGetRejectsExecutableSVG(t *testing.T) {
	srv := evilSite(t)
	defer srv.Close()

	dir := t.TempDir()
	f := New(dir, false)

	p, err := f.Get(context.Background(), srv.URL)
	if err == nil {
		t.Fatalf("带 onload 的 SVG 应该被拒，却拿到 %q", p)
	}
	if p != "" {
		t.Errorf("拒绝时不该返回路径，got %q", p)
	}

	// 缓存目录里不能留下 .svg
	matches, _ := filepath.Glob(filepath.Join(dir, "*.svg"))
	if len(matches) != 0 {
		t.Errorf("脏 SVG 不该落盘，却存在: %v", matches)
	}
}

// 干净 SVG 要正常抓到 —— 别把安全检查做成"所有 SVG 都拒"。
func TestGetAcceptsCleanSVG(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><html><head>`+
			`<link rel="icon" type="image/svg+xml" href="/ok.svg">`+
			`</head><body>x</body></html>`)
	})
	mux.HandleFunc("/ok.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">`+
			`<path d="M0 0h24v24H0z"/></svg>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	f := New(dir, false)
	p, err := f.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("干净 SVG 该抓到，却报错: %v", err)
	}
	if filepath.Ext(p) != ".svg" {
		t.Errorf("期望 .svg，got %s", p)
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		t.Errorf("缓存文件读不出来: %v", err)
	}
}

// 老版本落盘的脏 SVG 缓存：lookup 要能识别并删掉，不能直接拿去返回。
func TestLookupDropsPoisonedCache(t *testing.T) {
	dir := t.TempDir()
	f := New(dir, false)

	// 手工造一份脏缓存（模拟旧版本写进去的）
	k := key("http://example.invalid")
	dirty := filepath.Join(dir, k+".svg")
	if err := os.WriteFile(dirty, []byte(
		`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := f.lookup(k); got != "" {
		t.Errorf("脏缓存不该被 lookup 命中，got %q", got)
	}
	if _, err := os.Stat(dirty); !os.IsNotExist(err) {
		t.Error("脏缓存应该被删掉")
	}
}
