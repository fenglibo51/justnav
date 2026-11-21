// Package icons 在线图标库：按名字搜索、并把图标代理下来缓存。
//
// 为什么不让浏览器直接去 CDN 拿：
//  1. 家里的网络不一定通 jsDelivr（有时被墙/被限速），图标会整片空白；
//  2. 混了第三方域名，图片加载失败的原因排查起来很麻烦。
//
// 后端拿一次、落盘缓存，前端只认同源的 /api/icons/get?name=xxx，哪台设备打开都一样快。
//
// 数据源：homarr-labs/dashboard-icons（3500+ 个自建服务/软件图标，含别名）
//
//	索引    https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/metadata.json
//	图标    https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/<name>.svg
//	        （没有 svg 时退 png，再退 selfh.st/icons）
package icons

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"justnav/internal/svgscan"
)

const (
	metaURL    = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/metadata.json"
	svgCDN     = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/"
	pngCDN     = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/"
	selfhstCDN = "https://cdn.jsdelivr.net/gh/selfhst/icons/svg/"
	// 索引一天拉一次就够，图标本身不常变
	metaTTL = 24 * time.Hour
	// 单个图标最多 512KB，别被奇怪的文件打爆
	maxIconBytes = 512 << 10
)

// Entry 是搜索结果里的一项。
type Entry struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type metaItem struct {
	Base    string   `json:"base"`
	Aliases []string `json:"aliases"`
}

type Client struct {
	dataDir string
	http    *http.Client

	mu       sync.Mutex
	names    []string          // 全部图标名（排序后，便于稳定输出）
	alias    map[string]string // 别名 → 图标名
	loadedAt time.Time
	loadErr  error
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func New(dataDir string) *Client {
	return &Client{
		dataDir: dataDir,
		http:    &http.Client{Timeout: 15 * time.Second},
		alias:   map[string]string{},
	}
}

func (c *Client) cacheDir() string {
	d := filepath.Join(c.dataDir, "iconcache")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// ensureIndex 拉一次 metadata.json 建索引（失败不致命，搜不到而已）。
func (c *Client) ensureIndex(ctx context.Context) error {
	c.mu.Lock()
	if len(c.names) > 0 && time.Since(c.loadedAt) < metaTTL {
		err := c.loadErr
		c.mu.Unlock()
		return err
	}
	lastErr := c.loadErr
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "justnav/0.3")
	resp, err := c.http.Do(req)
	if err != nil {
		// 拉不到索引就沿用上次的（进程重启后第一次会没有）
		c.mu.Lock()
		c.loadErr = fmt.Errorf("拉取图标索引失败: %w", err)
		c.mu.Unlock()
		if len(c.names) > 0 {
			return nil
		}
		return lastErr
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.mu.Lock()
		c.loadErr = fmt.Errorf("图标索引返回 %d", resp.StatusCode)
		c.mu.Unlock()
		return c.loadErr
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var raw map[string]metaItem
	if err := json.Unmarshal(body, &raw); err != nil {
		c.mu.Lock()
		c.loadErr = fmt.Errorf("解析图标索引: %w", err)
		c.mu.Unlock()
		return c.loadErr
	}

	names := make([]string, 0, len(raw))
	alias := make(map[string]string, len(raw)*2)
	for k, v := range raw {
		if !nameRe.MatchString(k) {
			continue
		}
		names = append(names, k)
		alias[strings.ToLower(k)] = k
		for _, a := range v.Aliases {
			a = strings.ToLower(strings.TrimSpace(a))
			if a != "" {
				if _, exists := alias[a]; !exists {
					alias[a] = k
				}
			}
		}
	}
	sort.Strings(names)

	c.mu.Lock()
	c.names, c.alias, c.loadedAt, c.loadErr = names, alias, time.Now(), nil
	c.mu.Unlock()
	return nil
}

// Count 返回索引里的图标数量（给前端显示“3500+ 个图标”用）。
func (c *Client) Count(ctx context.Context) int {
	if err := c.ensureIndex(ctx); err != nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.names)
}

// Search 按名字/别名搜图标。排序：完全相等 > 前缀 > 包含，同档按名字长度。
func (c *Client) Search(ctx context.Context, q string, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	if err := c.ensureIndex(ctx); err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))

	c.mu.Lock()
	names := append([]string(nil), c.names...)
	alias := make(map[string]string, len(c.alias))
	for k, v := range c.alias {
		alias[k] = v
	}
	c.mu.Unlock()

	// 空查询：给一批常用图标，别让面板空着
	if q == "" {
		out := make([]Entry, 0, limit)
		for _, n := range names {
			if len(out) >= limit {
				break
			}
			out = append(out, Entry{Name: n, URL: "/api/icons/get?name=" + url.QueryEscape(n)})
		}
		return out, nil
	}

	type scored struct {
		name  string
		score int
	}
	seen := map[string]bool{}
	var hits []scored
	add := func(n string, s int) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		hits = append(hits, scored{name: n, score: s})
	}
	for _, n := range names {
		ln := strings.ToLower(n)
		switch {
		case ln == q:
			add(n, 0)
		case strings.HasPrefix(ln, q):
			add(n, 1)
		case strings.Contains(ln, q):
			add(n, 2)
		}
	}
	for a, n := range alias {
		if !strings.Contains(a, q) {
			continue
		}
		s := 3
		if a == q {
			s = 0
		} else if strings.HasPrefix(a, q) {
			s = 1
		}
		add(n, s)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		if len(hits[i].name) != len(hits[j].name) {
			return len(hits[i].name) < len(hits[j].name)
		}
		return hits[i].name < hits[j].name
	})
	out := make([]Entry, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		out = append(out, Entry{Name: h.name, URL: "/api/icons/get?name=" + url.QueryEscape(h.name)})
	}
	return out, nil
}

// Get 取图标字节流：先看磁盘缓存，没有再依次打三个源。
// 返回内容类型与是否命中缓存。
func (c *Client) Get(ctx context.Context, name string) ([]byte, string, error) {
	if !nameRe.MatchString(name) {
		return nil, "", fmt.Errorf("图标名不合法")
	}
	dir := c.cacheDir()
	for _, ext := range []string{"svg", "png"} {
		p := filepath.Join(dir, name+"."+ext)
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			// 老版本落盘的缓存没清洗过，读到脏的删掉，走下面的网络重拉
			if ext == "svg" && svgscan.Unsafe(b) {
				_ = os.Remove(p)
				continue
			}
			return b, contentType(ext), nil
		}
	}

	sources := []struct {
		url string
		ext string
	}{
		{svgCDN + name + ".svg", "svg"},
		{pngCDN + name + ".png", "png"},
		{selfhstCDN + name + ".svg", "svg"},
	}
	var lastErr error
	for _, s := range sources {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "justnav/0.3")
		req.Header.Set("Accept", "image/*")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s 返回 %d", s.url, resp.StatusCode)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes))
		resp.Body.Close()
		if err != nil || len(body) == 0 {
			lastErr = fmt.Errorf("%s 读取失败", s.url)
			continue
		}
		if s.ext == "svg" && svgscan.Unsafe(body) {
			lastErr = fmt.Errorf("%s 返回的 SVG 含可执行内容，已丢弃", s.url)
			continue
		}
		_ = os.WriteFile(filepath.Join(dir, name+"."+s.ext), body, 0o644)
		return body, contentType(s.ext), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("找不到图标 %s", name)
	}
	return nil, "", lastErr
}

func contentType(ext string) string {
	if ext == "png" {
		return "image/png"
	}
	return "image/svg+xml"
}
