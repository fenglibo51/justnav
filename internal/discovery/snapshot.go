// Package discovery 读取由 justnav-discovery 旁车生成的容器快照。
//
// 为什么要这么绕：Docker 的 socket 一旦挂进容器，就等于把宿主机 root 权限
// 交给了这个进程 —— 而 Docker API 没有"只读"这个概念，读列表和创建特权容器
// 走同一个 socket、同一套鉴权。`:ro` 只挡写文件，不挡 API 调用。
//
// 所以拆成两个容器：
//
//	justnav-discovery   唯一挂 socket 的，只做一件事 —— 采容器列表、过滤字段、写 JSON
//	justnav（本程序）    完全不碰 Docker，只读那个 JSON
//
// 即使 JustNav 被攻破，攻击者也拿不到 Docker API，只能读到一个经过白名单过滤的快照。
// 注意：Discovery 自己仍然持有完整 Docker 权限，这不是"彻底只读"，只是把
// 高风险面从「一个对公网/内网暴露的 Web 应用」缩到「一个没有网络服务的采集器」。
package discovery

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// SchemaVersion 快照格式版本。加字段可以，删字段必须升版本。
const SchemaVersion = 1

// Snapshot 是 Discovery 写出来的文件内容。
type Snapshot struct {
	SchemaVersion int         `json:"schemaVersion"`
	GeneratedAt   time.Time   `json:"generatedAt"`
	Containers    []Container `json:"containers"`
}

// Container 是经过白名单裁剪的容器信息。
//
// ★ 这里【故意】没有 Env / Mounts / HostConfig / NetworkSettings。
//   那些字段里通常全是数据库密码和 API key，采集器一个字节都不该往外写。
type Container struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Image  string            `json:"image"`
	State  string            `json:"state"`            // running / exited / ...
	Status string            `json:"status,omitempty"` // "Up 2 hours" 这类人话
	Ports  []Port            `json:"ports,omitempty"`
	Labels map[string]string `json:"labels,omitempty"` // 只含 nav.* 白名单键
}

// Port 一个端口映射。
type Port struct {
	Private  int    `json:"private"`
	Public   int    `json:"public,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

// Status 描述快照的可用状态。前端要靠它区分三种完全不同的"没数据"：
//
//	未启用   没配路径 —— 用户压根没开这个功能
//	已过期   文件在，但 generatedAt 太旧 —— 采集器挂了或停了
//	正常     数据新鲜
//
// 不能把三种混成一个"没有候选"的空列表 —— 用户会以为是"真没有容器"。
type Status struct {
	Enabled     bool      `json:"enabled"`
	Stale       bool      `json:"stale"`
	LastUpdated time.Time `json:"lastUpdated,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// Load 读快照并把超过 staleAfter 的判定为过期。
//
// path 为空 = 功能没启用，返回 Enabled=false 而不是 error —— 这是正常配置，
// 不该让调用方当成故障。
func Load(path string, staleAfter time.Duration) (*Snapshot, Status, error) {
	if path == "" {
		return nil, Status{Enabled: false, Reason: "未配置 JUSTNAV_DISCOVERY_FILE"}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在：启用了但采集器还没跑出第一份，算过期而不是报错
			return nil, Status{Enabled: true, Stale: true, Reason: "快照文件还没生成"}, nil
		}
		return nil, Status{Enabled: true, Reason: err.Error()}, err
	}
	defer f.Close()

	var snap Snapshot
	// 8MiB 上限：采集器那边也限了这个数，两边对齐，防止畸形文件撑爆内存
	if err := json.NewDecoder(io.LimitReader(f, 8<<20)).Decode(&snap); err != nil {
		return nil, Status{Enabled: true, Stale: true, Reason: "快照解析失败: " + err.Error()}, err
	}

	if snap.SchemaVersion > SchemaVersion {
		return nil, Status{Enabled: true, Stale: true,
			Reason: fmt.Sprintf("快照版本 %d 比程序支持的 %d 新，请升级 JustNav",
				snap.SchemaVersion, SchemaVersion)}, nil
	}

	age := time.Since(snap.GeneratedAt)
	if staleAfter > 0 && age > staleAfter {
		return &snap, Status{
			Enabled: true, Stale: true, LastUpdated: snap.GeneratedAt,
			Reason: fmt.Sprintf("快照已 %.0f 秒未更新（阈值 %.0f 秒），采集器可能停了",
				age.Seconds(), staleAfter.Seconds()),
		}, nil
	}

	return &snap, Status{Enabled: true, Stale: false, LastUpdated: snap.GeneratedAt}, nil
}
