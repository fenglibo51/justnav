// Package dockerx 把「容器快照」变成「待收录候选」。
//
// ★ 这个包里【没有任何网络代码】—— 不 import net、不 import net/http。
//
// 原因：Docker 的 socket 一旦挂进容器就等于交出宿主机 root（Docker API 没有
// 只读概念，读列表和创建特权容器走同一个 socket）。所以调 Docker API 这件事
// 被整个搬到了 cmd/justnav-discovery 那个独立二进制里，主程序只读它写出来的
// JSON 快照。这个包只做纯数据变换，编译进主程序的代码里不含任何发起 Docker
// 请求的能力 —— 就算主程序被攻破，攻击者也找不到可打的 Docker 端点。
//
// 见 internal/discovery/snapshot.go 里的完整说明。
package dockerx

import (
	"strconv"
	"strings"

	"justnav/internal/discovery"
)

// Suggested 一条待收录候选。
type Suggested struct {
	Container string `json:"container"`
	Image     string `json:"image"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	External  string `json:"external,omitempty"`
	Icon      string `json:"icon,omitempty"`
	Group     string `json:"group,omitempty"`
	Status    string `json:"status,omitempty"`
	// Source: label = 带 nav.* 标签；port = 兜底扫到的有端口映射的容器
	Source string `json:"source"`
	Port   int    `json:"port,omitempty"`
}

// 自身与代理不参与发现
var selfHints = []string{"justnav", "docker-proxy", "socket-proxy"}

// Suggest 从快照里的容器列表挑出候选。
//
// 两种来源：
//   - label 模式（精确）：容器带 nav.enable=true，名称/图标/分组/地址都从标签读
//   - port 模式（兜底）：不带任何标签，但有端口映射且在运行 —— 也能进「待收录」
//
// 兜底模式是为了「开箱即用」：不打标签也能在界面上勾选收录，
// 标签只是可选增强，不是使用前提。
//
// includeUnlabeled 控制是否启用端口兜底。
func Suggest(containers []discovery.Container, includeUnlabeled bool) []Suggested {
	out := make([]Suggested, 0, len(containers))
	for _, c := range containers {
		if isTrue(c.Labels["nav.hide"]) {
			continue
		}
		name := strings.TrimPrefix(c.Name, "/")
		if isSelf(name, c.Image) {
			continue
		}

		publicPort := firstPublicPort(c.Ports)

		// ① 带标签 → 精确收录
		if isTrue(c.Labels["nav.enable"]) {
			labelName := c.Labels["nav.name"]
			if labelName == "" {
				labelName = name
			}
			// 没写 nav.url 就【不】给地址。
			// 以前这里拼 http://127.0.0.1:<port>，那是访问者自己那台机器的回环地址，
			// 不是 NAS —— 从别的设备点开必然连不上。留空让前端用
			// JUSTNAV_INTERNAL_HOST 拼成 http://<内网IP>:<port>。
			url := c.Labels["nav.url"]
			out = append(out, Suggested{
				Container: name,
				Image:     c.Image,
				Name:      labelName,
				URL:       url,
				External:  c.Labels["nav.url.external"],
				Icon:      c.Labels["nav.icon"],
				Group:     c.Labels["nav.group"],
				Status:    c.Status,
				Source:    "label",
				Port:      publicPort,
			})
			continue
		}

		// ② 没标签但有端口映射 → 兜底候选（地址交给前端补主机名）
		if includeUnlabeled && c.State == "running" && publicPort > 0 {
			out = append(out, Suggested{
				Container: name,
				Image:     c.Image,
				Name:      name,
				Status:    c.Status,
				Source:    "port",
				Port:      publicPort,
			})
		}
	}
	return out
}

func isSelf(name, image string) bool {
	l := strings.ToLower(name + " " + image)
	for _, h := range selfHints {
		if strings.Contains(l, h) {
			return true
		}
	}
	return false
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func firstPublicPort(ports []discovery.Port) int {
	for _, p := range ports {
		if p.Public <= 0 {
			continue
		}
		// 只认 TCP。DNS 那种 53/udp 映射也带 public 端口，拼成
		// http://host:53 点开必然打不开；它常常还排在真正的网页端口前面，
		// 一旦被选中，卡片链接就是死的。Protocol 为空按 TCP 处理（多数
		// docker 端口本来就不写协议，默认 tcp）。
		if p.Protocol != "" && !strings.EqualFold(p.Protocol, "tcp") {
			continue
		}
		return p.Public
	}
	return 0
}

// ParsePort 给外部用的容错解析
func ParsePort(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
