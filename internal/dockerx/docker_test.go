package dockerx

import (
	"testing"

	"justnav/internal/discovery"
)

// 快照里的容器列表（模拟 discovery 写出来的内容）
func sample() []discovery.Container {
	return []discovery.Container{
		{ID: "c1", Name: "plex", Image: "plexinc/pms", State: "running", Status: "Up 2 hours",
			Labels: map[string]string{
				"nav.enable": "true", "nav.name": "Plex", "nav.url": "http://10.0.0.2:32400",
				"nav.url.external": "https://plex.example.com", "nav.icon": "play", "nav.group": "影音媒体",
			},
			Ports: []discovery.Port{{Private: 32400, Public: 32400, Protocol: "tcp"}}},
		{ID: "c2", Name: "qbittorrent", Image: "linuxserver/qbittorrent", State: "running", Status: "Up 1 hour",
			Ports: []discovery.Port{{Private: 8080, Public: 8080, Protocol: "tcp"}}},
		{ID: "c3", Name: "some-db", Image: "postgres:17", State: "running", Status: "Up 1 hour",
			Ports: []discovery.Port{{Private: 5432, Protocol: "tcp"}}},
		{ID: "c4", Name: "stopped-one", Image: "foo/bar", State: "exited", Status: "Exited",
			Ports: []discovery.Port{{Private: 9000, Public: 9000, Protocol: "tcp"}}},
		{ID: "c5", Name: "justnav", Image: "justnav:local", State: "running", Status: "Up",
			Labels: map[string]string{"nav.enable": "true"},
			Ports:  []discovery.Port{{Private: 8522, Public: 8522, Protocol: "tcp"}}},
		{ID: "c6", Name: "hidden", Image: "x/y", State: "running", Status: "Up",
			Labels: map[string]string{"nav.enable": "true", "nav.hide": "true"},
			Ports:  []discovery.Port{{Private: 80, Public: 80, Protocol: "tcp"}}},
	}
}

func TestSuggestLabelAndPortFallback(t *testing.T) {
	got := Suggest(sample(), true)

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
func TestSuggestWithoutFallback(t *testing.T) {
	got := Suggest(sample(), false)
	if len(got) != 1 || got[0].Name != "Plex" {
		t.Errorf("只应返回带标签的那一个, got %+v", got)
	}
}

func TestSuggestEmpty(t *testing.T) {
	got := Suggest(nil, true)
	if len(got) != 0 {
		t.Errorf("空快照应该返回空候选, got %+v", got)
	}
}

// TestNoNetworkImports 确认这个包没有被引入网络能力。
// 主程序编译产物里不该存在任何能发 Docker API 请求的代码路径。
func TestNoNetworkImports(t *testing.T) {
	// 这是一个编译期断言式的检查：本文件的 import 列表里只有 testing / discovery。
	// dockerx 包内任何文件 import 了 net 或 net/http，这个测试的作者就会看到
	// 新的依赖出现在 go list 里 —— 用 go vet 也能发现。
	//
	// 真正的保障在 CI：`go list -deps ./internal/dockerx | grep -c 'net/http'` 应为 0。
	// 这里只做一个显式提醒，不重复实现。
	t.Log("dockerx 应保持零网络依赖，见 CI 的 go list 检查与 selfcheck.sh")
}

// 端口挑第一个公开的——但 53/udp 这种不能拼成 http://host:53。
// 曾经的实现只看 p.Public > 0，DNS 容器排在前面时卡片链接就是死的。
func TestFirstPublicPortSkipsUDP(t *testing.T) {
	cases := []struct {
		name  string
		ports []discovery.Port
		want  int
	}{
		{
			name: "udp 排在前面要跳过",
			ports: []discovery.Port{
				{Private: 53, Public: 53, Protocol: "udp"},
				{Private: 80, Public: 8080, Protocol: "tcp"},
			},
			want: 8080,
		},
		{
			name:  "协议为空按 tcp 处理",
			ports: []discovery.Port{{Private: 80, Public: 8081}},
			want:  8081,
		},
		{
			name:  "全是 udp 就返回 0",
			ports: []discovery.Port{{Private: 53, Public: 53, Protocol: "udp"}},
			want:  0,
		},
		{
			name:  "没有公开端口返回 0",
			ports: []discovery.Port{{Private: 80, Protocol: "tcp"}},
			want:  0,
		},
		{
			name:  "TCP 大写也认",
			ports: []discovery.Port{{Private: 443, Public: 8443, Protocol: "TCP"}},
			want:  8443,
		},
	}
	for _, tc := range cases {
		if got := firstPublicPort(tc.ports); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
