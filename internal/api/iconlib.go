package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"justnav/internal/svgscan"
)

// handleIconSearch 在线图标库搜索：GET /api/icons/search?q=plex&limit=60
func (s *Server) handleIconSearch(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.storeFor(w, r); !ok {
		return
	}
	if s.IconLib == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "图标库未启用"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 60
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	items, err := s.IconLib.Search(ctx, q, limit)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"count": s.IconLib.Count(ctx),
		"items": items,
	})
}

// handleIconGet 图标代理：GET /api/icons/get?name=plex
// 服务端拉一次并落盘缓存，前端只认同源 URL —— 换设备/换网络都一样能用。
func (s *Server) handleIconGet(w http.ResponseWriter, r *http.Request) {
	if s.IconLib == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	name = strings.TrimSuffix(name, ".svg")
	name = strings.TrimSuffix(name, ".png")
	if name == "" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	body, ct, err := s.IconLib.Get(ctx, name)
	if err != nil {
		http.Error(w, "找不到图标", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ct)
	// 图标基本不变：缓存一周，省得每次开页面都来回打
	w.Header().Set("Cache-Control", "public, max-age=604800")
	// SVG 是唯一会被浏览器当「文档」渲染的图标格式：谁把 /api/icons/get
	// 的链接直接打开，浏览器就在 JustNav 自己的源下渲染它 —— 里面的脚本
	// 能读 cookie、能调写接口。icons.svgUnsafe 已经按 XML 解析结果拦了
	// script / on* 事件 / 伪协议，但那是黑名单，不能当唯一边界。
	//
	// 这条 CSP 把它关进沙箱：就算漏过黑名单，脚本也执行不了。PNG 是位图，
	// 没有执行能力，不需要。style-src 放开内联是为了让图标自己的样式生效。
	if strings.HasPrefix(ct, "image/svg") {
		w.Header().Set("Content-Security-Policy", svgscan.CSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	w.Write(body)
}
