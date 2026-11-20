package dockerx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeDocker(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			t.Errorf("意外路径: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
}

const sample = `[
  {"Names":["/plex"],"Image":"plexinc/pms","State":"running","Status":"Up 2 hours",
   "Labels":{"nav.enable":"true","nav.name":"Plex","nav.url":"http://10.0.0.2:32400",
             "nav.url.external":"https://plex.example.com","nav.icon":"play","nav.group":"影音媒体"},
   "Ports":[{"PrivatePort":32400,"PublicPort":32400}]},
  {"Names":["/qbittorrent"],"Image":"linuxserver/qbittorrent","State":"running","Status":"Up 1 hour",
   "Labels":{},"Ports":[{"PrivatePort":8080,"PublicPort":8080}]},
  {"Names":["/some-db"],"Image":"postgres:17","State":"running","Status":"Up 1 hour",
   "Labels":{},"Ports":[{"PrivatePort":5432}]},
  {"Names":["/stopped-one"],"Image":"foo/bar","State":"exited","Status":"Exited",
   "Labels":{},"Ports":[{"PrivatePort":9000,"PublicPort":9000}]},
  {"Names":["/justnav"],"Image":"justnav:local","State":"running","Status":"Up",
   "Labels":{"nav.enable":"true"},"Ports":[{"PrivatePort":8522,"PublicPort":8522}]},
  {"Names":["/hidden"],"Image":"x/y","State":"running","Status":"Up",
   "Labels":{"nav.enable":"true","nav.hide":"true"},"Ports":[{"PrivatePort":80,"PublicPort":80}]}
]`

func TestDiscoverLabelAndPortFallback(t *testing.T) {
	srv := fakeDocker(t, sample)
	defer srv.Close()

	got, err := Discover(context.Background(), "tcp://"+srv.Listener.Addr().String(), true)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]Suggested{}
	for _, x := range got {
		byName[x.Name] = x
	}

	// ① 带标签：名称/图标/分组/外网地址全部读到
	plex, ok := byName["Plex"]
	if !ok {
		t.Fatalf("带标签的容器应该被收录: %+v", got)
	}
	if plex.Source != "label" || plex.Icon != "play" || plex.Group != "影音媒体" ||
		plex.External != "https://plex.example.com" {
		t.Errorf("标签字段没读全: %+v", plex)
	}

	// ② 没标签但有端口映射 → 兜底候选
	qb, ok := byName["qbittorrent"]
	if !ok {
		t.Fatalf("有端口映射的容器应该进兜底候选: %+v", got)
	}
	if qb.Source != "port" || qb.Port != 8080 {
		t.Errorf("兜底候选字段不对: %+v", qb)
	}

	// ③ 没有端口映射的不进候选
	if _, bad := byName["some-db"]; bad {
		t.Error("没有端口映射的容器不该出现在候选里")
	}
	// ④ 没运行的不进候选
	if _, bad := byName["stopped-one"]; bad {
		t.Error("已停止的容器不该出现")
	}
	// ⑤ 自身不出现
	if _, bad := byName["justnav"]; bad {
		t.Error("justnav 自己不该出现在候选里")
	}
	// ⑥ nav.hide 主动排除
	if _, bad := byName["hidden"]; bad {
		t.Error("带 nav.hide=true 的应该排除")
	}
}

// 关掉兜底扫描时，只出带标签的
func TestDiscoverWithoutFallback(t *testing.T) {
	srv := fakeDocker(t, sample)
	defer srv.Close()

	got, err := Discover(context.Background(), "tcp://"+srv.Listener.Addr().String(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Plex" {
		t.Errorf("只应返回带标签的那一个, got %+v", got)
	}
}

func TestDiscoverBadHost(t *testing.T) {
	if _, err := Discover(context.Background(), "", true); err == nil {
		t.Error("空 host 应该报错")
	}
	if _, err := Discover(context.Background(), "ftp://x", true); err == nil {
		t.Error("不支持的 scheme 应该报错")
	}
}

func TestDiscoverErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"denied"}`))
	}))
	defer srv.Close()

	_, err := Discover(context.Background(), "tcp://"+srv.Listener.Addr().String(), true)
	if err == nil {
		t.Fatal("403 应该报错，而不是静默返回空")
	}
}

// 确保 JSON 结构与真实 Docker API 对得上（字段名不能自己编）
func TestPayloadShape(t *testing.T) {
	var list []container
	if err := json.Unmarshal([]byte(sample), &list); err != nil {
		t.Fatalf("样例 JSON 解析失败: %v", err)
	}
	if len(list) != 6 {
		t.Errorf("样例应解析出 6 个容器, got %d", len(list))
	}
}
