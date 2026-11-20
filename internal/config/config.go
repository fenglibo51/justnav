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

	// Docker 发现：留空表示禁用。
	// 推荐填 socket-proxy 地址（如 tcp://docker-proxy:2375），
	// 不要直接把 /var/run/docker.sock 挂进容器 —— 等于给出宿主机 root。
	DockerHost string

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
		DockerHost:             dockerHost(),
		ProbeEnabled:           envBool("JUSTNAV_PROBE", true),
		ProbeInterval:          time.Duration(envInt("JUSTNAV_PROBE_INTERVAL_SEC", 60)) * time.Second,
		ProbeTimeout:           time.Duration(envInt("JUSTNAV_PROBE_TIMEOUT_SEC", 4)) * time.Second,
		ProbeInsecure:          envBool("JUSTNAV_PROBE_INSECURE", true),
		ProbeThreshold:         envInt("JUSTNAV_PROBE_THRESHOLD", 2),
		DockerIncludeUnlabeled: envBool("JUSTNAV_DOCKER_SCAN_ALL", true),
		InternalHost:           env("JUSTNAV_INTERNAL_HOST", ""),
		AuthEnabled:            envBool("JUSTNAV_AUTH", true),
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

// dockerHost 决定 Docker 发现走哪条路：
//   - 显式配了 JUSTNAV_DOCKER_HOST 就听配置的；
//   - 没配但容器里挂了 /var/run/docker.sock，就直接挂 socket 这条（2026-11-13 起
//     默认走法，不再需要 docker-socket-proxy 旁车）；
//   - 都没有就空着，前端「待收录」会提示未启用。
func dockerHost() string {
	if v := strings.TrimSpace(os.Getenv("JUSTNAV_DOCKER_HOST")); v != "" {
		return v
	}
	for _, p := range []string{"/var/run/docker.sock", "/run/docker.sock"} {
		if st, err := os.Stat(p); err == nil && st.Mode()&os.ModeSocket != 0 {
			return "unix://" + p
		}
	}
	return ""
}
