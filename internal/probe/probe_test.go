package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOnlineOn2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := New(time.Minute, 2*time.Second, true, 2)
	p.Check(context.Background(), []Target{{ID: "a", URL: srv.URL}})

	st, ok := p.All()["a"]
	if !ok || !st.Online {
		t.Fatalf("2xx 应该判在线: %+v", st)
	}
	if st.Code != 200 {
		t.Errorf("code = %d", st.Code)
	}
}

// 关键用例：401/403/404 都算在线 —— 服务活着，只是要登录或路径不同。
// 如果这里判成离线，几乎每个自建服务都会显示「未响应」。
func TestAuthAnd404StillOnline(t *testing.T) {
	cases := map[string]int{
		"401": http.StatusUnauthorized,
		"403": http.StatusForbidden,
		"404": http.StatusNotFound,
		"302": http.StatusFound,
	}
	for name, code := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer srv.Close()

			p := New(time.Minute, 2*time.Second, true, 2)
			p.Check(context.Background(), []Target{{ID: "x", URL: srv.URL}})
			if st := p.All()["x"]; !st.Online {
				t.Errorf("%s 应判在线, got %+v", name, st)
			}
		})
	}
}

func Test5xxIsOffline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	p := New(time.Minute, 2*time.Second, true, 1)
	p.Check(context.Background(), []Target{{ID: "b", URL: srv.URL}})
	if st := p.All()["b"]; st.Online {
		t.Errorf("5xx 应判离线, got %+v", st)
	}
}

func TestConnRefusedIsOffline(t *testing.T) {
	p := New(time.Minute, 800*time.Millisecond, true, 1)
	p.Check(context.Background(), []Target{{ID: "c", URL: "http://127.0.0.1:59999"}})
	st := p.All()["c"]
	if st.Online {
		t.Errorf("连不上应判离线, got %+v", st)
	}
	if st.Err == "" {
		t.Error("离线时应带错误原因")
	}
}

// HEAD 不被支持时要退回 GET，否则这类服务会永远显示离线。
func TestHeadNotAllowedFallsBackToGet(t *testing.T) {
	var headCalls, getCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			atomic.AddInt32(&headCalls, 1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		atomic.AddInt32(&getCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := New(time.Minute, 2*time.Second, true, 2)
	p.Check(context.Background(), []Target{{ID: "d", URL: srv.URL}})

	if st := p.All()["d"]; !st.Online {
		t.Errorf("HEAD 405 后应回退 GET 并判在线, got %+v", st)
	}
	if headCalls != 1 || getCalls != 1 {
		t.Errorf("调用次数 head=%d get=%d", headCalls, getCalls)
	}
}

// 条目被删掉后，状态表里不能再留着它。
func TestRemovedTargetDisappears(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	p := New(time.Minute, 2*time.Second, true, 2)
	p.Check(context.Background(), []Target{{ID: "a", URL: srv.URL}, {ID: "b", URL: srv.URL}})
	if len(p.All()) != 2 {
		t.Fatalf("应有 2 条")
	}
	p.Check(context.Background(), []Target{{ID: "a", URL: srv.URL}})
	if _, ok := p.All()["b"]; ok {
		t.Error("已删除的条目不该留在状态表里")
	}
}

func TestEmptyURLSkipped(t *testing.T) {
	p := New(time.Minute, time.Second, true, 2)
	p.Check(context.Background(), []Target{{ID: "empty", URL: ""}})
	if len(p.All()) != 0 {
		t.Error("空地址不该产生状态")
	}
}

// 抖动抑制：阈值 2 时，第一次失败只是 pending，第二次才真离线。
func TestFlappingSuppressedUntilThreshold(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := New(time.Minute, 2*time.Second, true, 2)
	p.Check(context.Background(), []Target{{ID: "f", URL: srv.URL}})
	if st := p.All()["f"]; !st.Online || st.Pending {
		t.Fatalf("初始应在线: %+v", st)
	}

	fail.Store(true)
	p.Check(context.Background(), []Target{{ID: "f", URL: srv.URL}})
	st := p.All()["f"]
	if !st.Online || !st.Pending {
		t.Errorf("第一次失败应保持在线且 pending, got %+v", st)
	}
	if st.Fails != 1 {
		t.Errorf("fails 应为 1, got %d", st.Fails)
	}

	p.Check(context.Background(), []Target{{ID: "f", URL: srv.URL}})
	st = p.All()["f"]
	if st.Online {
		t.Errorf("连续两次失败应判离线, got %+v", st)
	}
	if st.Fails != 2 {
		t.Errorf("fails 应为 2, got %d", st.Fails)
	}

	// 恢复后计数要清零
	fail.Store(false)
	p.Check(context.Background(), []Target{{ID: "f", URL: srv.URL}})
	st = p.All()["f"]
	if !st.Online || st.Pending || st.Fails != 0 {
		t.Errorf("恢复后应完全清零, got %+v", st)
	}
}
