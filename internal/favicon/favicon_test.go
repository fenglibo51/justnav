package favicon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// 1x1 PNG
var pngBytes = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
	0x42, 0x60, 0x82,
}

func TestFetchesAndCaches(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.png" {
			hits++
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := New(dir, true)

	p1, err := f.Get(context.Background(), srv.URL+"/some/page")
	if err != nil {
		t.Fatalf("应抓到图标: %v", err)
	}
	if _, err := os.Stat(p1); err != nil {
		t.Fatalf("落盘文件不存在: %v", err)
	}

	// 第二次必须走缓存，不再请求源站
	p2, err := f.Get(context.Background(), srv.URL+"/other/page")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Errorf("同一站点应命中同一缓存文件: %s vs %s", p1, p2)
	}
	if hits != 1 {
		t.Errorf("缓存在命中时不该再请求源站, hits=%d", hits)
	}
}

func TestNotAnImageIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>404 page</html>"))
	}))
	defer srv.Close()

	f := New(t.TempDir(), true)
	if _, err := f.Get(context.Background(), srv.URL); err == nil {
		t.Error("HTML 404 页不该被当成图标存下来")
	}
}

func TestRejectsNonHTTP(t *testing.T) {
	f := New(t.TempDir(), true)
	for _, u := range []string{"file:///etc/passwd", "ftp://x/y", "javascript:alert(1)"} {
		if _, err := f.Get(context.Background(), u); err == nil {
			t.Errorf("%s 应该被拒绝", u)
		}
	}
}

func TestTooLargeRejected(t *testing.T) {
	big := make([]byte, maxBytes+100)
	copy(big, pngBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	f := New(t.TempDir(), true)
	if _, err := f.Get(context.Background(), srv.URL); err == nil {
		t.Error("超过体积上限的图标应该被拒绝")
	}
}
