// Package dockerx 从容器里发现可收录的服务。
//
// 两种来源：
//   - label 模式（精确）：容器带 nav.enable=true，名称/图标/分组/地址都从标签读
//   - port 模式（兜底）：不带任何标签，但有端口映射且在运行 —— 也能进「待收录」
//
// 兜底模式是为了「开箱即用」：不打标签也能在界面上勾选收录，
// 标签只是可选增强，不是使用前提。
//
// 安全要求：只读、只列举。
// 不要把 /var/run/docker.sock 直接挂进容器 —— `:ro` 只约束文件系统挂载，
// 不约束 Docker API，socket 一通就等于宿主机 root。
// 正确做法是走 docker-socket-proxy 旁车，只放开 CONTAINERS 的 GET 端点。
package dockerx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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

type container struct {
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Ports  []struct {
		PrivatePort int `json:"PrivatePort"`
		PublicPort  int `json:"PublicPort"`
	} `json:"Ports"`
}

// 自身与代理不参与发现
var selfHints = []string{"justnav", "docker-proxy", "socket-proxy"}

// Discover 列出候选服务。includeUnlabeled 控制是否启用端口兜底。
func Discover(ctx context.Context, host string, includeUnlabeled bool) ([]Suggested, error) {
	base, client, err := newClient(host)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/containers/json?all=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 Docker API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("Docker API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var list []container
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("解析容器列表: %w", err)
	}

	out := make([]Suggested, 0, len(list))
	for _, c := range list {
		if isTrue(c.Labels["nav.hide"]) {
			continue
		}
		containerName := strings.TrimPrefix(firstOrEmpty(c.Names), "/")
		if isSelf(containerName, c.Image) {
			continue
		}

		publicPort := firstPublicPort(c.Ports)

		// ① 带标签 → 精确收录
		if isTrue(c.Labels["nav.enable"]) {
			name := c.Labels["nav.name"]
			if name == "" {
				name = containerName
			}
			url := c.Labels["nav.url"]
			if url == "" && publicPort > 0 {
				url = fmt.Sprintf("http://127.0.0.1:%d", publicPort)
			}
			out = append(out, Suggested{
				Container: containerName,
				Image:     c.Image,
				Name:      name,
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
				Container: containerName,
				Image:     c.Image,
				Name:      containerName,
				Status:    c.Status,
				Source:    "port",
				Port:      publicPort,
			})
		}
	}
	return out, nil
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

func newClient(host string) (string, *http.Client, error) {
	if host == "" {
		return "", nil, fmt.Errorf("未配置 JUSTNAV_DOCKER_HOST")
	}
	switch {
	case strings.HasPrefix(host, "unix://"):
		socket := strings.TrimPrefix(host, "unix://")
		tr := &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		}
		return "http://docker", &http.Client{Transport: tr, Timeout: 10 * time.Second}, nil
	case strings.HasPrefix(host, "tcp://"):
		return "http://" + strings.TrimPrefix(host, "tcp://"), &http.Client{Timeout: 10 * time.Second}, nil
	case strings.HasPrefix(host, "http://"), strings.HasPrefix(host, "https://"):
		return strings.TrimRight(host, "/"), &http.Client{Timeout: 10 * time.Second}, nil
	default:
		return "", nil, fmt.Errorf("无法识别的 JUSTNAV_DOCKER_HOST: %q", host)
	}
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func firstOrEmpty(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}

func firstPublicPort(ports []struct {
	PrivatePort int `json:"PrivatePort"`
	PublicPort  int `json:"PublicPort"`
}) int {
	for _, p := range ports {
		if p.PublicPort > 0 {
			return p.PublicPort
		}
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
