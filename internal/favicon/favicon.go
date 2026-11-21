// Package favicon 抓取并缓存站点图标。
//
// 为什么放后端：浏览器直接跨域取 favicon 会被 CORS / 混合内容挡住，
// 而且每次打开都请求外部站点很慢。这里抓一次落本地，之后走自己的缓存。
package favicon

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const maxBytes = 256 << 10 // 图标不该超过 256KB

var candidates = []string{"/favicon.ico", "/favicon.png", "/apple-touch-icon.png"}

// 现代 Web 应用的图标基本不放在根路径，而是 <link rel="icon" href="/assets/xxx-hash.ico">。
// 只试那三个固定路径会大量 miss —— 这就是「明明有图标却显示首字母」的主因（2026-11-19）。
var (
	linkIconRe = regexp.MustCompile(`(?is)<link\b[^>]*\brel\s*=\s*["']?[^"'>]*icon[^"'>]*["']?[^>]*>`)
	hrefRe     = regexp.MustCompile(`(?is)\bhref\s*=\s*["']([^"']+)["']`)
)

// discoverIconLinks 抓首页 HTML，把里面声明的图标地址解析成绝对 URL。
func (f *Fetcher) discoverIconLinks(ctx context.Context, base string) []string {
	c, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, base+"/", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; justnav/0.1)")
	req.Header.Set("Accept", "text/html,*/*;q=0.8")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil
	}
	html := string(body)
	baseURL, err := url.Parse(base + "/")
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, tag := range linkIconRe.FindAllString(html, -1) {
		hm := hrefRe.FindStringSubmatch(tag)
		if len(hm) < 2 {
			continue
		}
		href := strings.TrimSpace(hm[1])
		if href == "" || strings.HasPrefix(href, "data:") {
			continue
		}
		abs := href
		if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") {
			u, err := url.Parse(href)
			if err != nil {
				continue
			}
			abs = baseURL.ResolveReference(u).String()
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

type Fetcher struct {
	dir    string
	client *http.Client

	mu       sync.Mutex
	inflight map[string]bool
}

/* ---------- SSRF 防护 ----------

只挡「绝对不是导航目标」的地址：

  169.254.0.0/16        链路本地，含云元数据 169.254.169.254（里面有临时凭据）
  fd00:ec2::254         IPv6 的云元数据
  metadata.google.internal

内网 192.168.* / 10.* / 172.16.* 一律放行 —— JustNav 本来就是 NAS 导航页，
那些正是它要访问的服务。一刀切封掉等于所有内网服务都显示离线，功能就废了。
能进到你内网的人本来就能直接访问那些管理口，favicon 这条路径不额外增加多少风险。

防护点有两处，缺一不可：

  ① DialContext   先解析 DNS，再检查解析出来的 IP。
                  只查 URL 字符串会被「自己的域名解析到 169.254.169.254」绕过（DNS 重绑定）。
  ② CheckRedirect 每次跳转重新走一遍检查。
                  否则第一次地址合法、跳转后打到管理端点就绕过去了。
*/
var blockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"169.254.0.0/16", "fd00:ec2::254/128"} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

var blockedHosts = map[string]bool{
	"metadata.google.internal": true,
	"metadata":                 true,
}

func blockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4 // IPv4-mapped IPv6 转回 IPv4 再判
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// blockedTarget 静态判断一个 URL 是否指向禁访目标。
func blockedTarget(u *url.URL) bool {
	if u == nil {
		return true
	}
	if blockedHosts[strings.ToLower(u.Hostname())] {
		return true
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && blockedIP(ip) {
		return true
	}
	return false
}

func New(dir string, insecure bool) *Fetcher {
	tr := &http.Transport{
		Proxy: nil,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecure, // #nosec G402 —— 内网自签证书是常态
		},
		MaxIdleConnsPerHost: 2,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("解析不到 %s", host)
			}
			for _, ipa := range ips {
				if blockedIP(ipa.IP) {
					return nil, fmt.Errorf("拒绝连接 %s：该地址不允许抓取", ipa.IP)
				}
			}
			// 用已解析的 IP 直连，避免二次解析被换掉
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	return &Fetcher{
		dir: dir,
		client: &http.Client{
			Transport: tr,
			Timeout:   8 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("重定向次数过多")
				}
				if blockedTarget(req.URL) {
					return errors.New("重定向目标不允许")
				}
				return nil
			},
		},
		inflight: map[string]bool{},
	}
}

func key(rawURL string) string {
	sum := sha1.Sum([]byte(rawURL))
	return hex.EncodeToString(sum[:])
}

// Get 返回本地缓存路径；没有就现抓一次。抓不到返回错误（前端自己回退字母块）。
func (f *Fetcher) Get(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("只支持 http/https")
	}
	if u.Host == "" {
		return "", errors.New("地址缺少主机名")
	}
	// 入口先静态判一次，跟 DNS 解析后的 IP 校验、逐跳重定向校验保持一致。
	// 字面 IP（169.254.169.254 / fd00:ec2::254 这类云元数据）和 metadata
	// 主机名在这里就能直接拒，不用先发请求。blockedNets 只含云元数据和链路
	// 本地，RFC1918 不拦 —— 局域网 IP 是正当的导航目标。
	if blockedTarget(u) {
		return "", errors.New("这个地址不允许抓取")
	}

	base := u.Scheme + "://" + u.Host
	k := key(base)
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return "", err
	}
	if p := f.lookup(k); p != "" {
		return p, nil
	}

	// 同一个站点并发只抓一次
	f.mu.Lock()
	if f.inflight[k] {
		f.mu.Unlock()
		return "", errors.New("正在抓取中")
	}
	f.inflight[k] = true
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.inflight, k)
		f.mu.Unlock()
	}()

	save := func(body []byte, ct string) (string, bool) {
		ext := extFor(ct, body)
		if ext == "" {
			return "", false // 大概率是 HTML 404 页，不是图标
		}
		p := filepath.Join(f.dir, k+ext)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			return "", false
		}
		_ = os.Chmod(p, 0o644)
		return p, true
	}

	// 第一轮：三个最常见的固定路径（命中率最高、最快）
	for _, path := range candidates {
		body, ct, err := f.fetch(ctx, base+path)
		if err != nil || len(body) == 0 {
			continue
		}
		if p, ok := save(body, ct); ok {
			return p, nil
		}
	}

	// 第二轮：解析首页 HTML 里声明的 <link rel="icon">（SPA / 带 hash 的资源路径）
	for _, iconURL := range f.discoverIconLinks(ctx, base) {
		body, ct, err := f.fetch(ctx, iconURL)
		if err != nil || len(body) == 0 {
			continue
		}
		if p, ok := save(body, ct); ok {
			return p, nil
		}
	}

	return "", fmt.Errorf("没找到可用的 favicon")
}

// Refresh 强制重抓：先删掉这个站点的旧缓存，再走一遍完整抓取。
// 编辑弹窗里的「重新抓取」按钮用这个 —— 之前 miss 过的站点不会自动重试，
// 想让它再试一次只能手动触发（2026-11-19）。
func (f *Fetcher) Refresh(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("只支持 http/https")
	}
	if u.Host == "" {
		return "", errors.New("地址缺少主机名")
	}
	// 入口先静态判一次，跟 DNS 解析后的 IP 校验、逐跳重定向校验保持一致。
	// 字面 IP（169.254.169.254 / fd00:ec2::254 这类云元数据）和 metadata
	// 主机名在这里就能直接拒，不用先发请求。blockedNets 只含云元数据和链路
	// 本地，RFC1918 不拦 —— 局域网 IP 是正当的导航目标。
	if blockedTarget(u) {
		return "", errors.New("这个地址不允许抓取")
	}
	k := key(u.Scheme + "://" + u.Host)
	for _, ext := range []string{".ico", ".png", ".jpg", ".svg", ".webp"} {
		_ = os.Remove(filepath.Join(f.dir, k+ext))
	}
	return f.Get(ctx, rawURL)
}

func (f *Fetcher) lookup(k string) string {
	for _, ext := range []string{".ico", ".png", ".jpg", ".svg", ".webp"} {
		p := filepath.Join(f.dir, k+ext)
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			return p
		}
	}
	return ""
}

func (f *Fetcher) fetch(ctx context.Context, u string) ([]byte, string, error) {
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(c, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; justnav/0.1)")
	req.Header.Set("Accept", "image/*,*/*;q=0.8")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("状态 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxBytes {
		return nil, "", errors.New("图标过大")
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// extFor 判断字节流到底是不是图片，返回对应扩展名；不是图片返回空。
func extFor(contentType string, b []byte) string {
	if len(b) < 4 {
		return ""
	}
	switch {
	case b[0] == 0x00 && b[1] == 0x00 && (b[2] == 0x01 || b[2] == 0x02):
		return ".ico"
	case string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return ".png"
	case b[0] == 0xFF && b[1] == 0xD8:
		return ".jpg"
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return ".webp"
	}
	if strings.Contains(contentType, "svg") || strings.Contains(string(b[:min(200, len(b))]), "<svg") {
		return ".svg"
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
