// justnav-discovery —— 唯一持有 Docker Socket 的进程。
//
// 它只做一件事：定时 GET /containers/json?all=true，把需要的字段挑出来，
// 原子写成一个 JSON 文件，供 justnav 主程序读取。
//
// 为什么单独拆一个二进制：
//
//	主程序是个 Web 应用，有登录、有前端、有对外端口 —— 攻击面大。
//	让它持有 Docker Socket 等于把宿主机 root 交给一个 Web 应用。
//	Docker API 没有只读概念（读列表和创建特权容器走同一个 socket），
//	所以只能把「接触 socket」这件事隔离到一个最小进程里。
//
// 这个二进制刻意做到：
//
//	没有 HTTP 服务、没有监听端口（配 network_mode: none 跑）
//	没有 shell、没有 docker CLI、不执行任何外部命令
//	API 路径写死，不接受任何外部输入来决定请求什么
//	只输出白名单字段，Env / Mounts / HostConfig 一个字节都不写出去
//	采集失败保留上次快照，不把「采集挂了」表现成「容器全没了」
//
// ★ 但它自己仍然持有完整的 Docker 权限。这不是「真只读」，
//   只是把高风险面从「一个对外提供 Web 服务的应用」缩到「一个哑采集器」。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"justnav/internal/discovery"
)

const (
	defaultSock     = "/var/run/docker.sock"
	defaultOut      = "/out/containers.json"
	defaultInterval = 15
	apiPath         = "/containers/json?all=true" // 写死，不接受外部指定
	maxRespBytes    = 8 << 20                     // 8MiB，和读取端对齐
	reqTimeout      = 5 * time.Second
)

// allowedLabels 是唯一会被写进快照的标签。
//
// ★ 绝对不能用「全部标签都带上」的做法 —— 很多部署工具会把数据库密码、
//   API key、镜像仓库凭据塞进容器标签里（比如某些 compose 的 x- 扩展、
//   Traefik 的 basicauth、Watchtower 的通知 token）。
//   白名单之外的标签，一个都不往外写。
var allowedLabels = map[string]bool{
	"nav.enable":        true,
	"nav.hide":          true,
	"nav.name":          true,
	"nav.url":           true,
	"nav.url.external":  true,
	"nav.icon":          true,
	"nav.group":         true,
}

// dockerContainer 只声明我们要读的字段，其余一律丢弃。
type dockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[discovery] ")

	sock := env("JUSTNAV_DISCOVERY_SOCK", defaultSock)
	out := env("JUSTNAV_DISCOVERY_OUT", defaultOut)
	interval := envInt("JUSTNAV_DISCOVERY_INTERVAL_SEC", defaultInterval)
	if interval < 5 {
		interval = 5 // 别让人把它调成 1 秒去打 Docker
	}

	client := unixClient(sock)

	// 启动先采一次，不用等第一个 tick
	if err := collectOnce(client, out); err != nil {
		log.Printf("首次采集失败（保留旧快照）：%v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	t := time.NewTicker(time.Duration(interval) * time.Second)
	defer t.Stop()
	log.Printf("开始采集：socket=%s 输出=%s 间隔=%ds", sock, out, interval)

	for {
		select {
		case <-ctx.Done():
			log.Print("收到退出信号，停止采集")
			return
		case <-t.C:
			if err := collectOnce(client, out); err != nil {
				// 失败不退出：Docker 可能临时重启，旧快照继续可用
				log.Printf("采集失败（保留旧快照）：%v", err)
			}
		}
	}
}

// collectOnce 采一次并写盘。
func collectOnce(client *http.Client, outPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), reqTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+apiPath, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连接 Docker socket: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 只读前 512 字节，别把整个错误页塞进日志
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("Docker API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var raw []dockerContainer
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRespBytes)).Decode(&raw); err != nil {
		return fmt.Errorf("解析容器列表: %w", err)
	}

	snap := discovery.Snapshot{
		SchemaVersion: discovery.SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Containers:    make([]discovery.Container, 0, len(raw)),
	}

	for _, c := range raw {
		if c.ID == "" {
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		snap.Containers = append(snap.Containers, discovery.Container{
			ID:     truncate(c.ID, 64),
			Name:   truncate(name, 128),
			Image:  truncate(c.Image, 200),
			State:  truncate(c.State, 32),
			Status: truncate(c.Status, 96),
			Ports:  pickPorts(c.Ports),
			Labels: pickLabels(c.Labels),
		})
	}

	return writeAtomic(outPath, snap)
}

// pickPorts 只保留端口号和协议。
func pickPorts(in []struct {
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}) []discovery.Port {
	out := make([]discovery.Port, 0, len(in))
	for _, p := range in {
		if p.PrivatePort <= 0 {
			continue
		}
		proto := p.Type
		if proto == "" {
			proto = "tcp"
		}
		out = append(out, discovery.Port{
			Private:  p.PrivatePort,
			Public:   p.PublicPort,
			Protocol: proto,
		})
	}
	return out
}

// pickLabels 只留白名单内的键。
func pickLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, 4)
	for k, v := range in {
		if !allowedLabels[k] {
			continue
		}
		out[k] = truncate(v, 512)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// writeAtomic 先写同目录临时文件再 rename —— 读端永远看不到写一半的文件。
func writeAtomic(path string, snap discovery.Snapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录: %w", err)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".containers-*.json")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// 失败路径上把半成品删掉，成功路径上它已经被 rename 走了
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		// 某些文件系统（含部分 overlay / 网络卷）不支持 fsync，不当成致命错误
		if !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
			tmp.Close()
			return fmt.Errorf("fsync: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// 0644：主程序以另一个 uid 读，必须可读；里面没有敏感内容（只有白名单字段）
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换快照: %w", err)
	}
	log.Printf("已更新快照：%d 个容器", len(snap.Containers))
	return nil
}

// unixClient 走 unix socket 的 http 客户端。Proxy 显式置 nil，
// 否则会去读 HTTP_PROXY 环境变量，把 unix 请求发到代理上去。
func unixClient(sock string) *http.Client {
	return &http.Client{
		Timeout: reqTimeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
			DisableCompression: true,
		},
	}
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("环境变量 %s=%q 不是正整数，用默认值 %d", k, v, def)
		return def
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
