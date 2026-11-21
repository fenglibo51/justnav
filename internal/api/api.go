// Package api 提供 HTTP 接口与前端静态资源。
package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"context"

	"justnav/internal/auth"
	"justnav/internal/config"
	"justnav/internal/discovery"
	"justnav/internal/dockerx"
	"justnav/internal/favicon"
	"justnav/internal/icons"
	"justnav/internal/probe"
	"justnav/internal/downloader"
	"justnav/internal/store"
	"justnav/internal/wallpaper"
	"justnav/internal/weather"

	"justnav/internal/svgscan"
)

// Version 由构建时注入：-ldflags "-X justnav/internal/api.Version=1.0.3"
// 直接 go build 不注入就显示 dev —— 开发期间别在这里手改数字，
// 只有真正发版（打 tag、CI 构建）才注入，否则一天能涨好几个版本号。
var Version = "dev"

// 1x1 透明 PNG：favicon 抓不到时回这个，避免 404 污染控制台
var transparentPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
	0x42, 0x60, 0x82,
}

// Deps 把所有依赖打包，避免以后加一个组件就改一次构造函数签名。
type Deps struct {
	Cfg     config.Config
	Store   *store.Registry
	Wall    *wallpaper.Manager
	Probe   *probe.Prober
	Favicon *favicon.Fetcher
	IconLib *icons.Client
	Auth    *auth.Manager
	Weather *weather.Client
	Web     fs.FS
}

type Server struct {
	Deps
	started time.Time
}

func New(d Deps) *Server {
	return &Server{Deps: d, started: time.Now()}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)

	mux.HandleFunc("GET /api/users", s.handleUsersList)
	mux.HandleFunc("POST /api/users", s.handleUserAdd)
	mux.HandleFunc("PUT /api/users/password", s.handleUserPassword)
	mux.HandleFunc("PUT /api/users/username", s.handleUserRename)
	mux.HandleFunc("DELETE /api/users", s.handleUserDelete)

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/items", s.handleList)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/sysinfo", s.handleSysinfo)
	mux.HandleFunc("PUT /api/items/order", s.handleItemOrder)
	mux.HandleFunc("GET /api/favicon", s.handleFavicon)
	mux.HandleFunc("GET /api/icons/search", s.handleIconSearch)
	mux.HandleFunc("GET /api/icons/get", s.handleIconGet)
	mux.HandleFunc("POST /api/items/bulk", s.handleItemsBulk)
	mux.HandleFunc("POST /api/items", s.handleUpsert)
	mux.HandleFunc("DELETE /api/items", s.handleDelete)
	mux.HandleFunc("POST /api/settings", s.handleSettings)

	mux.HandleFunc("POST /api/groups", s.handleGroupAdd)
	mux.HandleFunc("PUT /api/groups", s.handleGroupRename)
	mux.HandleFunc("PUT /api/groups/order", s.handleGroupOrder)
	mux.HandleFunc("DELETE /api/groups", s.handleGroupDelete)

	mux.HandleFunc("GET /api/wallpaper", s.handleWallpaperInfo)
	mux.HandleFunc("GET /api/wallpaper/img", s.handleWallpaperImage)
	mux.HandleFunc("POST /api/wallpaper/refresh", s.handleWallpaperRefresh)
	mux.HandleFunc("POST /api/wallpaper/upload", s.handleWallpaperUpload)

	mux.HandleFunc("GET /api/inbox", s.handleInbox)

	// 下载器：列表 + 实时状态 / 检测单台连接
	mux.HandleFunc("GET /api/downloaders", s.handleDownloaders)
	mux.HandleFunc("POST /api/downloaders/test", s.handleDownloaderTest)

	mux.HandleFunc("GET /api/weather", s.handleWeather)
	mux.HandleFunc("GET /api/weather/forecast", s.handleWeatherForecast)
	mux.HandleFunc("GET /api/weather/search", s.handleWeatherSearch)

	// 静态资源：embed 的前端
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("GET /assets/", http.FileServer(http.FS(s.Web)))

	return withLogging(securityHeaders(s.csrfGuard(s.authGate(mux))))
}

// ---------- health ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_, hasWall := s.Wall.Current()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"version":     Version,
		"siteName":    s.Cfg.SiteName,
		"uptimeSec":   int(time.Since(s.started).Seconds()),
		"wallpaper":   s.Cfg.WallpaperEnabled,
		"wallpaperAt": hasWall,
		"docker":      s.Cfg.DiscoveryFile != "", // 键名保持 docker 兼容前端；值表示"自动发现是否启用"
	})
}

// ---------- items ----------

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	d := st.Snapshot()
	items := st.List()
	if items == nil {
		items = []store.Item{} // 空库要返回 []，不能是 null（前端 .map 会炸）
	}
	// 下载器密码不能跟着 settings 一起吐出去
	sett := d.Settings
	sett.Downloaders = store.MaskDownloaders(sett.Downloaders)
	writeJSON(w, http.StatusOK, map[string]any{
		"groups":   d.Groups,
		"items":    items,
		"settings": sett,
	})
}

func (s *Server) handleUpsert(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	var it store.Item
	if err := decodeJSON(r, &it); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if it.Name == "" {
		writeErr(w, http.StatusBadRequest, "name 不能为空")
		return
	}
	if err := st.Upsert(it); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": st.List()})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}
	ok, err := st.Delete(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "条目不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": st.List()})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	var p store.Settings
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.NetMode != "" && p.NetMode != "in" && p.NetMode != "out" {
		writeErr(w, http.StatusBadRequest, "netMode 只能是 in 或 out")
		return
	}
	if err := st.SetSettings(p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 兼容老前端：widget:"none" 等价于清空整个列表
	if p.Widget == "none" {
		if err := st.ClearWidget(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	snap := st.Snapshot().Settings
	snap.Downloaders = store.MaskDownloaders(snap.Downloaders)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": snap})
}

// ---------- favicon ----------

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		http.Error(w, "缺少 url", http.StatusBadRequest)
		return
	}
	if s.Favicon == nil {
		http.Error(w, "图标抓取未启用", http.StatusNotFound)
		return
	}
	var path string
	var err error
	if r.URL.Query().Get("force") == "1" {
		path, err = s.Favicon.Refresh(r.Context(), raw)
	} else {
		path, err = s.Favicon.Get(r.Context(), raw)
	}
	if err != nil {
		// ⚠️ 不返回 404：源站没有图标是常态，而每个 404 都会在浏览器控制台
		// 刷一条 "Failed to load resource"。这里回 200 + 头标记，前端跳过即可。
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Favicon", "miss")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Write(transparentPNG)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=604800")
	// 第二层防御：SVG 会被浏览器当文档渲染，而 /api/favicon 跟页面同源。
	// favicon 包已经在落盘时用 svgscan.Unsafe 拦了可执行内容，但那是黑名单，
	// 不能当唯一边界 —— 万一漏过，这条 CSP 让它在沙箱里跑不起来，读不到
	// cookie、调不了写接口。PNG/ICO 等位图没有执行能力，不需要。
	if strings.HasSuffix(path, ".svg") {
		w.Header().Set("Content-Security-Policy", svgscan.CSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	http.ServeFile(w, r, path)
}

// ---------- 条目排序 ----------

func (s *Server) handleItemOrder(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs   []string          `json:"ids"`
		Items []store.Placement `json:"items"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var err error
	if len(req.Items) > 0 {
		err = st.SetLayout(req.Items)
	} else {
		err = st.SetItemOrder(req.IDs)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": st.List()})
}

// ---------- 壁纸上传 ----------

func (s *Server) handleWallpaperUpload(w http.ResponseWriter, r *http.Request) {
	// 请求体上限比图片上限略大（multipart 有边界开销）
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "上传内容过大或格式不对")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少文件字段 file")
		return
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, 10<<20+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取上传内容失败")
		return
	}
	if len(data) > 10<<20 {
		writeErr(w, http.StatusRequestEntityTooLarge, "图片超过 10MB")
		return
	}

	u, okU := userFrom(r)
	if !okU {
		writeErr(w, http.StatusUnauthorized, "未登录")
		return
	}
	wpx, hpx, err := s.Wall.SaveCustom(u.ID, data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "width": wpx, "height": hpx,
		"imageUrl": "/api/wallpaper/img?custom=1",
	})
}

// ---------- 批量导入 ----------

func (s *Server) handleItemsBulk(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	var req struct {
		Items []store.Item `json:"items"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "items 为空")
		return
	}
	if len(req.Items) > 500 {
		writeErr(w, http.StatusBadRequest, "单次最多导入 500 条")
		return
	}
	added, updated, err := st.BulkUpsert(req.Items)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "added": added, "updated": updated,
		"items": st.List(),
	})
}

// ---------- status ----------

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r)
	// 探测结果用 "<uid>:<itemID>" 做 key，这里只取当前用户的，并去掉前缀
	all := map[string]probe.Status{}
	if s.Probe != nil {
		all = s.Probe.All()
	}
	res := map[string]probe.Status{}
	prefix := u.ID + ":"
	for k, v := range all {
		if strings.HasPrefix(k, prefix) {
			res[strings.TrimPrefix(k, prefix)] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   s.Cfg.ProbeEnabled,
		"intervalS": int(s.Cfg.ProbeInterval.Seconds()),
		"status":    res,
		"host":      s.collectHostStats(),
	})
}

// ---------- groups ----------

func (s *Server) handleGroupAdd(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := st.AddGroup(req.Name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": st.Snapshot().Groups})
}

func (s *Server) handleGroupRename(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := st.RenameGroup(req.From, req.To); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": st.Snapshot().Groups})
}

func (s *Server) handleGroupOrder(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	var req struct {
		Groups []string `json:"groups"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := st.SetGroupOrder(req.Groups); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": st.Snapshot().Groups})
}

func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	name := q.Get("name")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "缺少 name")
		return
	}
	moved, err := st.DeleteGroup(name, q.Get("moveTo"))
	if err != nil {
		// 组里还有条目时返回 409，让前端去问迁移目标
		code := http.StatusBadRequest
		if moved == 0 && strings.Contains(err.Error(), "需要先指定迁移") {
			code = http.StatusConflict
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "moved": moved, "groups": st.Snapshot().Groups,
	})
}

// ---------- wallpaper ----------

func (s *Server) handleWallpaperInfo(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Date  string `json:"date"`
		Bytes int64  `json:"bytes"`
	}
	var cached []item
	for _, m := range s.Wall.Cached() {
		cached = append(cached, item{Date: m.Date, Bytes: m.Bytes})
	}
	cur, ok := s.Wall.Current()
	u, _ := userFrom(r)
	resp := map[string]any{
		"enabled":   s.Cfg.WallpaperEnabled,
		"available": ok,
		"cached":    cached,
		"custom":    u.ID != "" && s.Wall.HasCustom(u.ID),
	}
	if ok {
		resp["meta"] = cur
		resp["imageUrl"] = "/api/wallpaper/img"
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWallpaperImage(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.Wall.Current()
	if !ok {
		http.Error(w, "还没有壁纸", http.StatusNotFound)
		return
	}

	if r.URL.Query().Get("custom") == "1" {
		u, _ := userFrom(r)
		// 自定义壁纸是私人的：没登录就看不到（登录页背景用每日壁纸即可）
		if u.ID == "" || !s.Wall.HasCustom(u.ID) {
			http.Error(w, "还没有自定义壁纸", http.StatusNotFound)
			return
		}
		// 自定义图可能被重新上传覆盖，不能强缓存
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, s.Wall.CustomPath(u.ID))
		return
	}

	meta := cur
	// 支持 ?date=yyyymmdd 取缓存里的历史壁纸；只认缓存目录里真实存在的文件
	if d := r.URL.Query().Get("date"); d != "" {
		matched := false
		for _, c := range s.Wall.Cached() {
			if c.Date == d {
				meta = c
				matched = true
				break
			}
		}
		if !matched {
			http.Error(w, "没有这个日期的壁纸", http.StatusNotFound)
			return
		}
	}

	// 同一天内这张图不会变，允许强缓存，减少 NAS 重复读盘
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Wallpaper-Date", meta.Date)
	if meta.Title != "" {
		w.Header().Set("X-Wallpaper-Title", meta.Title)
	}
	http.ServeFile(w, r, s.Wall.FilePath(meta))
}

func (s *Server) handleWallpaperRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.WallpaperEnabled {
		writeErr(w, http.StatusServiceUnavailable, "壁纸功能未启用")
		return
	}
	ctx := r.Context()
	meta, err := s.Wall.FetchDaily(ctx)
	if err != nil {
		// 抓取失败不算致命：前端应静默回退纯色底
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if n, err := s.Wall.Cleanup(s.Cfg.WallpaperKeepDays, s.Cfg.WallpaperMaxBytes); err != nil {
		log.Printf("壁纸清理失败: %v", err)
	} else if n > 0 {
		log.Printf("壁纸清理：删除 %d 张旧图", n)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "meta": meta})
}

// ---------- inbox ----------

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	// 读 justnav-discovery 写出来的快照。主程序不碰 Docker Socket。
	// 三种"没数据"要分清楚，否则用户会以为是容器真的没了：
	//   未启用（没配路径） / 已过期（采集器挂了） / 正常但没有候选
	snap, dstat, loadErr := discovery.Load(s.Cfg.DiscoveryFile, s.Cfg.DiscoveryStale)

	if !dstat.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"stale":   false,
			"reason":  dstat.Reason,
			"items":   []dockerx.Suggested{},
		})
		return
	}
	if loadErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": true, "stale": true,
			"reason": dstat.Reason,
			"items":  []dockerx.Suggested{},
		})
		return
	}

	var list []dockerx.Suggested
	if snap != nil {
		list = dockerx.Suggest(snap.Containers, s.Cfg.DockerIncludeUnlabeled)
	}
	// 已经在导航里的容器不再提示
	have := map[string]bool{}
	for _, it := range st.List() {
		have[strings.ToLower(it.Name)] = true
	}
	filtered := make([]dockerx.Suggested, 0, len(list))
	for _, x := range list {
		if have[strings.ToLower(x.Name)] {
			continue
		}
		filtered = append(filtered, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"stale":   dstat.Stale,
		"reason":  dstat.Reason,
		// 采集时间给前端判断"数据是不是很久没更新了"
		"lastUpdated": dstat.LastUpdated,
		"items":       filtered,
		// 兜底候选没有地址，前端用 hostHint 拼 http://<host>:<port>
		"hostHint": s.Cfg.InternalHost,
	})
}

// ---------- 账号管理（仅管理员） ----------

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	u, ok := userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "未登录")
		return auth.User{}, false
	}
	if !u.IsAdmin() {
		writeErr(w, http.StatusForbidden, "只有管理员能管理账号")
		return auth.User{}, false
	}
	return u, true
}

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": s.Auth.List()})
}

func (s *Server) handleUserAdd(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.Auth.Add(req.Name, req.Password, req.Role)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u, "users": s.Auth.List()})
}

func (s *Server) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	me, ok := userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "未登录")
		return
	}
	var req struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		req.ID = me.ID // 不给 id 就是改自己的密码
	}
	if req.ID != me.ID && !me.IsAdmin() {
		writeErr(w, http.StatusForbidden, "只能改自己的密码")
		return
	}
	if err := s.Auth.SetPassword(req.ID, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "self": req.ID == me.ID})
}

// handleUserRename 改账号名（2026-11-20）
// 不碰密码、不换 SessionTag，所以改完当前会话继续有效，不用重新登录。
func (s *Server) handleUserRename(w http.ResponseWriter, r *http.Request) {
	me, ok := userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "未登录")
		return
	}
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		req.ID = me.ID // 不给 id 就是改自己的账号名
	}
	if req.ID != me.ID && !me.IsAdmin() {
		writeErr(w, http.StatusForbidden, "只能改自己的账号名")
		return
	}
	if err := s.Auth.Rename(req.ID, req.Name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"name": strings.TrimSpace(req.Name),
		"self": req.ID == me.ID,
	})
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	me, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if err := s.Auth.Remove(id, me.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 连同这个人的导航数据一起删掉（不可逆，前端要二次确认）
	if err := s.Store.RemoveData(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "users": s.Auth.List()})
}

// ---------- 认证 ----------

type ctxKey int

const ctxUser ctxKey = 0

func userFrom(r *http.Request) (auth.User, bool) {
	u, ok := r.Context().Value(ctxUser).(auth.User)
	return u, ok
}

// storeFor 取「当前登录用户」的数据存储。没登录直接 401。
func (s *Server) storeFor(w http.ResponseWriter, r *http.Request) (*store.Store, auth.User, bool) {
	u, ok := userFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "未登录")
		return nil, auth.User{}, false
	}
	st, err := s.Store.For(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, auth.User{}, false
	}
	return st, u, true
}

// 不需要登录就能访问的路径：
//
//	/                    登录页本身（前端会就地显示登录框）
//	/api/auth/*          登录相关
//	/api/health          探活
//	/api/wallpaper/img   登录页背景要用它 —— 否则登录前背景是空的
var publicPaths = map[string]bool{
	"/":                true,
	"/api/auth/status": true,
	"/api/auth/login":  true,
	"/api/auth/logout": true,
	"/api/health":      true,
	// 登录页背景要用壁纸：img 是图片本体，/api/wallpaper 是元信息（标题/日期/缓存列表），
	// 都不含敏感内容，放在白名单里。
	"/api/wallpaper":     true,
	"/api/wallpaper/img": true,
}

func (s *Server) authGate(next http.Handler) http.Handler {
	if s.Auth == nil || !s.Cfg.AuthEnabled {
		// 关掉登录 = 单机自用。这里必须注入一个身份，否则所有业务接口
		// 都拿不到用户、全部 401，页面直接不可用（P2-9）。
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.User{ID: store.LocalUserID, Name: "本机", Role: auth.RoleAdmin}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser, u)))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 静态资源（favicon / apple-touch-icon）必须免登录：浏览器请求这些
		// 的时候往往还没登录，甚至登录页本身就是未登录态。拦下来会导致
		// 标签页图标 401、iOS 添加到主屏拿不到图标。
		// 里面只有图标文件，没有任何业务数据。
		if publicPaths[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(auth.CookieName); err == nil {
			if uid, ok := s.Auth.VerifySession(c.Value); ok {
				if u, exists := s.Auth.Get(uid); exists {
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser, u)))
					return
				}
			}
		}
		writeErr(w, http.StatusUnauthorized, "未登录")
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	authed := false
	var me *auth.Public
	if s.Auth != nil {
		if c, err := r.Cookie(auth.CookieName); err == nil {
			if uid, ok := s.Auth.VerifySession(c.Value); ok {
				if u, exists := s.Auth.Get(uid); exists {
					p := auth.Public{ID: u.ID, Name: u.Name, Role: u.Role}
					authed, me = true, &p
				}
			}
		}
	}
	resp := map[string]any{
		"enabled": s.Cfg.AuthEnabled,
		"authed":  authed,
		"multi":   true,
	}
	if me != nil {
		resp["user"] = me
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusServiceUnavailable, "认证未启用")
		return
	}
	ip := clientIP(r)
	if !s.Auth.AllowAttempt(ip) {
		writeErr(w, http.StatusTooManyRequests, "尝试次数过多，请 5 分钟后再试")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, ok := s.Auth.Authenticate(req.Username, req.Password)
	if !ok {
		// 记用户名和密码长度（不记密码本身），用来区分「用户名不对」和「密码打错」
		log.Printf("登录失败 username=%q 密码长度=%d ip=%s", req.Username, len(req.Password), ip)
		s.Auth.RecordFailure(ip)
		writeErr(w, http.StatusUnauthorized, "账号或密码不正确")
		return
	}
	s.Auth.ResetFailures(ip)

	token, ttl, err := s.Auth.IssueSession(user.ID, req.Remember)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cookie := &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.Cfg.CookieSecure,
	}
	if req.Remember {
		cookie.MaxAge = int(ttl.Seconds())
	}
	http.SetCookie(w, cookie)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "user": auth.Public{ID: user.ID, Name: user.Name, Role: user.Role},
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Cfg.CookieSecure,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// clientIP 优先取反代传来的 X-Forwarded-For（Lucky 会带），否则用直连地址。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

// ---------- 天气 ----------

// ---------- 下载器 ----------

// handleDownloaders 一次拉全部下载器的实时状态。多台并发查，单台超时不拖累别的。
func (s *Server) handleDownloaders(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	list := st.Snapshot().Settings.Downloaders
	stats := make([]downloader.Stats, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		wg.Add(1)
		go func(i int, c store.Downloader) {
			defer wg.Done()
			stats[i] = downloader.Fetch(c)
		}(i, c)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"configured": store.MaskDownloaders(list), // 密码已抹
		"stats":      stats,
	})
}

// handleDownloaderTest 检测连接。密码可以留空 —— 编辑已有下载器时前端不回传密码，
// 按 ID 从已存配置里取。
func (s *Server) handleDownloaderTest(w http.ResponseWriter, r *http.Request) {
	var c store.Downloader
	if err := decodeJSON(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if c.Pass == "" && c.ID != "" {
		if st, _, ok := s.storeFor(w, r); ok {
			for _, d := range st.Snapshot().Settings.Downloaders {
				if d.ID == c.ID {
					c.Pass = d.Pass
					if c.User == "" {
						c.User = d.User
					}
					break
				}
			}
		}
	}
	if c.Type == "" {
		writeErr(w, http.StatusBadRequest, "先选类型（qb / tr）")
		return
	}
	if c.URL == "" {
		writeErr(w, http.StatusBadRequest, "先填地址")
		return
	}
	res := downloader.Test(c)
	writeJSON(w, http.StatusOK, map[string]any{"ok": res.OK, "stats": res})
}

func (s *Server) handleWeather(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	set := st.Snapshot().Settings.Weather
	if set.City == "" || (set.Lat == 0 && set.Lon == 0) {
		// 没配城市：前端只显示时间日期，这一块不显示
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "needCity": true})
		return
	}
	if s.Weather == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "天气未启用"})
		return
	}
	// 主源 4s + 备用源 6s，最坏情况要 10s 才轮完，给 12s 别把自己的 ctx 先掐了
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	now, err := s.Weather.Current(ctx, set.City, set.Lat, set.Lon)
	if err != nil {
		// 拿不到天气不影响导航，前端隐藏这一块
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "weather": now})
}

// handleWeatherForecast 右侧栏天气卡片用的 5 天预报 + 今天详情。
// 数据源只有 TWC 有（中国天气网/open-meteo 的日高低温都不全），拿不到就返回 ok:false，
// 前端把这张卡收起来，不影响导航。
func (s *Server) handleWeatherForecast(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	set := st.Snapshot().Settings.Weather
	if set.City == "" || (set.Lat == 0 && set.Lon == 0) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "needCity": true})
		return
	}
	if s.Weather == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "天气未启用"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	// 拿一次实况，一是为了失败时能早退，二是今天的图标要用实况那个
	now, err := s.Weather.Current(ctx, set.City, set.Lat, set.Lon)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	fc, err := s.Weather.FetchForecast(ctx, set.City, set.Lat, set.Lon, now.Code)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	fc.Now.Feels = now.Feels
	if fc.Now.Humidity == 0 {
		fc.Now.Humidity = now.Humidity
	}
	fc.Now.Visibility = now.Visibility
	fc.Now.Pressure = now.Pressure
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "forecast": fc})
}

func (s *Server) handleWeatherSearch(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.storeFor(w, r); !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	place, err := s.Weather.Geocode(ctx, q)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "place": place})
}

// ---------- 静态 ----------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	raw, err := fs.ReadFile(s.Web, "index.html")
	if err != nil {
		http.Error(w, "index.html 缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// ⚠️ 用 no-store 而不是 no-cache：
	// no-cache 只是"每次要校验"，部分浏览器（尤其 Safari）会直接用内存里的旧页，
	// 表现就是"我明明改了，他刷新还是老样子"。no-store 直接禁止任何缓存。
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Write(raw)
}

// ---------- 辅助 ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "error": msg})
}

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("请求体为空")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// statusWriter 只为日志记状态码，其余行为全部透传给原 ResponseWriter。
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// securityHeaders 给每个响应加一组安全头。
//
// CSP 里 script-src 留了 'unsafe-inline'：前端是单文件 index.html，脚本和内联样式
// 全写在页面里，去掉就直接白屏。这一条挡不住「预埋在内联脚本里的注入」，
// 但能挡住外部脚本加载、iframe 嵌套、表单外发这些更省事的攻击路径。
// XSS 的真正防线在 esc() 转义（P0-1），这里只是加一层。
//
// img-src 放行 http: 和 https:：图标来自内网各服务的 http 地址，必须放。
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob: https: http:; " +
		"font-src 'self' data:; " +
		"media-src 'self' blob:; " +
		"connect-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		// 别让浏览器猜类型：本来返回 JSON 的接口被当成 HTML 执行就是 XSS
		h.Set("X-Content-Type-Options", "nosniff")
		// 别把带用户 id 的 URL 通过 Referer 漏给外站
		h.Set("Referrer-Policy", "same-origin")
		// 老浏览器不认 CSP frame-ancestors
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// csrfGuard 挡住跨站发起的写请求。
//
// 登录态走 Cookie，浏览器会在任何站点发起的请求里自动带上它 ——
// 别的网页只要往 /api/items 发个 POST，就能替用户改数据。
//
// 判定用 Origin（现代浏览器一定会带），没有就退到 Sec-Fetch-Site。
// 两个都没有的，是 curl 这类非浏览器客户端，放行：那种场景本来就没有
// 「受害者的 Cookie」这回事。
func (s *Server) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !s.originAllowed(origin, r) {
				writeErr(w, http.StatusForbidden, "跨站请求被拒绝")
				return
			}
		} else if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
			if site != "same-origin" && site != "none" {
				writeErr(w, http.StatusForbidden, "跨站请求被拒绝")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed 判断 Origin 是不是自己的地址。
//
// 比对 Host 而不是完整 URL：反代后面的协议和端口在程序这侧看不准，
// 但 Host 头会原样传过来。域名和反代端口不一致时，用
// JUSTNAV_ALLOWED_ORIGINS 补白名单（逗号分隔，写完整 origin）。
func (s *Server) originAllowed(origin string, r *http.Request) bool {
	for _, a := range s.Cfg.AllowedOrigins {
		if strings.EqualFold(strings.TrimRight(origin, "/"), strings.TrimRight(a, "/")) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		// 静态资源不刷日志，只记 API
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// 带状态码：以前只记方法和耗时，「登录失败到底是 401 还是 200 但
			// cookie 没存住」完全看不出来，排查只能靠猜。2026-11-20
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.code, time.Since(start).Round(time.Millisecond))
		}
	})
}
