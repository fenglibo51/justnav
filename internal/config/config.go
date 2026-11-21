// Package config 读取运行配置。
// 设计原则：全部来自环境变量，带可用默认值，不写死任何内网地址（项目会公开）。
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// 监听端口
	Port string
	// 数据目录（存导航条目 JSON 与壁纸缓存），必须是可写目录
	DataDir string
	// 站点标题
	SiteName string

	// 壁纸
	WallpaperEnabled  bool
	WallpaperKeepDays int
	WallpaperMaxBytes int64

	// Docker 自动发现：读 justnav-discovery 旁车写出来的快照文件。
	//
	// ★ 主程序【不持有 Docker Socket】，也不再支持 JUSTNAV_DOCKER_HOST。
	//   那条路能直连 Docker API，而 Docker API 没有只读概念 ——
	//   读容器列表和创建特权容器走同一个 socket，等于把宿主机 root 交给
	//   一个对外的 Web 应用。所以调 API 的能力整个搬进了 discovery 二进制，
	//   主程序只认这个 JSON 文件。
	//
	// 留空 = 未启用，功能界面上显示「未启用」，不是故障。
	DiscoveryFile string
	// 快照超过这么久没更新就判定过期。采集器默认 15 秒一次，这里留 4 倍余量。
	DiscoveryStale time.Duration

	// 状态探测
	ProbeEnabled  bool
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	// 内网自签证书极常见，默认跳过 TLS 校验
	ProbeInsecure bool
	// 连续失败几次才判离线。默认 2，用来压掉偶发超时造成的页面抖动。
	ProbeThreshold int

	// 登录认证
	AuthEnabled  bool
	// AllowedOrigins 是 CSRF 白名单：反代改了域名/端口、Origin 和 Host 对不上时用
	AllowedOrigins []string
	AuthUser     string
	AuthPassword string // 由环境变量提供；非空时以它为准（方便 compose 固定/重置密码）
	// 走 HTTPS 反代时置 true，cookie 才会带 Secure
	CookieSecure bool

	// 内网主机名/IP：给「待收录」的兜底候选拼地址用。
	// 不设的话前端只能用当前访问域名 —— 走反代时会拼出
	// http://nav.example.com:25600 这种打不开的地址。
	InternalHost string

	// Docker 发现：是否把「有端口映射但没打标签」的容器也列为候选。
	// 默认 true —— 这样不打标签也能开箱即用。
	DockerIncludeUnlabeled bool
}

func Load() Config {
	return Config{
		Port:                   env("JUSTNAV_PORT", "8522"),
		DataDir:                env("JUSTNAV_DATA_DIR", "./data"),
		SiteName:               env("JUSTNAV_SITE_NAME", "JustNav"),
		WallpaperEnabled:       envBool("JUSTNAV_WALLPAPER", true),
		WallpaperKeepDays:      envInt("JUSTNAV_WALLPAPER_KEEP_DAYS", 30),
		WallpaperMaxBytes:      envInt64("JUSTNAV_WALLPAPER_MAX_BYTES", 300*1024*1024),
		DiscoveryFile:          env("JUSTNAV_DISCOVERY_FILE", ""),
		DiscoveryStale:         time.Duration(envInt("JUSTNAV_DISCOVERY_STALE_SEC", 60)) * time.Second,
		ProbeEnabled:           envBool("JUSTNAV_PROBE", true),
		ProbeInterval:          time.Duration(envInt("JUSTNAV_PROBE_INTERVAL_SEC", 60)) * time.Second,
		ProbeTimeout:           time.Duration(envInt("JUSTNAV_PROBE_TIMEOUT_SEC", 4)) * time.Second,
		ProbeInsecure:          envBool("JUSTNAV_PROBE_INSECURE", true),
		ProbeThreshold:         envInt("JUSTNAV_PROBE_THRESHOLD", 2),
		DockerIncludeUnlabeled: envBool("JUSTNAV_DOCKER_SCAN_ALL", true),
		InternalHost:           env("JUSTNAV_INTERNAL_HOST", ""),
		AuthEnabled:            envBool("JUSTNAV_AUTH", true),
		AllowedOrigins:         envList("JUSTNAV_ALLOWED_ORIGINS"),
		AuthUser:               env("JUSTNAV_USER", "admin"),
		AuthPassword:           os.Getenv("JUSTNAV_PASSWORD"),
		CookieSecure:           envBool("JUSTNAV_COOKIE_SECURE", false),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envList 读逗号分隔的列表，空字符串得到 nil（不是 [""]）。
func envList(k string) []string {
	raw := strings.TrimSpace(os.Getenv(k))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envInt64(k string, def int64) int64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

