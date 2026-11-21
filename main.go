package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // 把时区库编进二进制，scratch 镜像里 TZ 才会生效

	"justnav/internal/api"
	"justnav/internal/auth"
	"justnav/internal/config"
	"justnav/internal/favicon"
	"justnav/internal/icons"
	"justnav/internal/probe"
	"justnav/internal/store"
	"justnav/internal/wallpaper"
	"justnav/internal/weather"
)

//go:embed all:web
var webFS embed.FS

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[justnav] ")

	cfg := config.Load()

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录 %s 失败: %v", cfg.DataDir, err)
	}

	// 每个账号一个独立数据目录：<data>/users/<uid>/
	stReg := store.NewRegistry(cfg.DataDir)

	wallDir := cfg.DataDir + "/wallpaper"
	// 自定义壁纸按用户存：<data>/users/<uid>/wallpaper.jpg
	wm := wallpaper.NewManager(wallDir, cfg.DataDir+"/users")
	if err := wm.LoadCurrent(); err != nil {
		log.Printf("读取壁纸元信息失败（忽略）: %v", err)
	}

	// 状态探测：探「当前模式下真正会用的那个地址」
	pr := probe.New(cfg.ProbeInterval, cfg.ProbeTimeout, cfg.ProbeInsecure, cfg.ProbeThreshold)

	// favicon 抓取与缓存（走同一个 TLS 策略：内网自签默认放行）
	fav := favicon.New(cfg.DataDir+"/favicon", cfg.ProbeInsecure)

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("前端资源挂载失败: %v", err)
	}

	// 认证：账号密码持久化在 <data>/auth.json
	authMgr, generatedPwd, err := auth.New(cfg.DataDir, cfg.AuthUser, cfg.AuthPassword)
	if err != nil {
		log.Fatalf("初始化认证失败: %v", err)
	}

	// 单用户时代的数据（data/nav.json）归到第一个管理员名下，不丢
	if admins := authMgr.List(); len(admins) > 0 {
		adminID := ""
		for _, u := range admins {
			if u.Role == auth.RoleAdmin {
				adminID = u.ID
				break
			}
		}
		if adminID != "" {
			if moved, err := stReg.MigrateLegacy(cfg.DataDir, adminID); err != nil {
				log.Printf("迁移旧导航数据失败（忽略）: %v", err)
			} else if moved {
				log.Printf("已把旧的 data/nav.json 迁移到账号 %s 名下", adminID)
			}
			if moved, err := stReg.MigrateLegacyWallpaper(cfg.DataDir, adminID); err != nil {
				log.Printf("迁移旧自定义壁纸失败（忽略）: %v", err)
			} else if moved {
				log.Printf("已把旧的 custom.jpg 迁移到账号 %s 名下", adminID)
			}
		}
	}
	if generatedPwd != "" {
		log.Printf("⚠️  首次启动，已创建管理员：%s  密码：%s  —— 登录后请立刻改掉", cfg.AuthUser, generatedPwd)
		log.Printf("    这行只打印一次。密码存在 <data>/users.json 里，之后在「设置 → 改密」修改即可")
		log.Printf("    （JUSTNAV_PASSWORD 只在没有任何账号时用于初始化，不会覆盖已有密码）")
	}
	if !cfg.AuthEnabled {
		log.Printf("⚠️  认证已关闭（JUSTNAV_AUTH=false）—— 不要把写接口暴露到公网")
	}

	srv := api.New(api.Deps{
		Cfg: cfg, Store: stReg, Wall: wm, Probe: pr,
		Favicon: fav, Auth: authMgr, Weather: weather.New(), Web: web,
		IconLib: icons.New(cfg.DataDir),
	})
	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// 壁纸每日后台任务：启动时补一次，之后每 6 小时检查（同一天不会重复下载）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if cfg.WallpaperEnabled {
		go wallpaperLoop(ctx, cfg, wm)
	}

	// 状态探测循环：每轮现取条目，增删改自动跟上
	if cfg.ProbeEnabled {
		go pr.Run(ctx, func() []probe.Target {
			// 遍历所有账号的条目；key 用 "<uid>:<itemID>" 避免跨账号撞 ID
			out := []probe.Target{}
			for _, pu := range authMgr.List() {
				st, err := stReg.For(pu.ID)
				if err != nil {
					continue
				}
				mode := st.Snapshot().Settings.NetMode
				for _, it := range st.List() {
					u := it.Internal
					if mode == "out" && it.External != "" {
						u = it.External
					}
					out = append(out, probe.Target{ID: pu.ID + ":" + it.ID, URL: u})
				}
			}
			return out
		})
		log.Printf("状态探测已启用：每 %s 一轮，超时 %s", cfg.ProbeInterval, cfg.ProbeTimeout)
	}

	go func() {
		log.Printf("监听 :%s | 数据目录 %s | 壁纸 %v | Docker 发现 %q",
			cfg.Port, cfg.DataDir, cfg.WallpaperEnabled, cfg.DiscoveryFile)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务退出: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("收到退出信号，正在关闭…")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancelShutdown()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭超时: %v", err)
	}
}

func wallpaperLoop(ctx context.Context, cfg config.Config, wm *wallpaper.Manager) {
	fetch := func() {
		// 后台任务不继承请求上下文，单独给 30 秒预算
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		meta, err := wm.FetchDaily(c)
		if err != nil {
			// 抓不到就保持现状，绝不影响导航本身
			log.Printf("壁纸抓取失败（沿用现有）: %v", err)
			return
		}
		log.Printf("壁纸就绪: %s %s %dx%d %.1fMB",
			meta.Date, meta.Title, meta.Width, meta.Height, float64(meta.Bytes)/1048576)
		if n, err := wm.Cleanup(cfg.WallpaperKeepDays, cfg.WallpaperMaxBytes); err == nil && n > 0 {
			log.Printf("壁纸清理：删除 %d 张旧图", n)
		}
	}

	fetch()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fetch()
		}
	}
}
