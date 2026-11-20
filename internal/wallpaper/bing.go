// Package wallpaper 负责每日 4K 壁纸的抓取、缓存与清理。
//
// 数据源只有 Bing 每日 UHD —— 2026-11-12 实测：
//   - 免 key，最近 8 天全部能取到 3840x2160 真 4K（3.3-3.6MB）
//   - 必须用 _UHD 后缀那版；追加 &w=3840 也能出 4K 但只有 0.8MB，是强压缩画质
//
// ⚠️ 抓下来的图有版权（Getty / 摄影师投稿）。只存本地缓存，绝不进 git 仓库。
package wallpaper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	bingAPI = "https://www.bing.com/HPImageArchive.aspx?format=js&idx=0&n=1&mkt=zh-CN"
	// 单张上限，超过直接放弃（防止被 CDN 返回的超大文件拖死）
	maxDownload = 15 << 20
	metaFile    = "meta.json"
	// 自定义上传的壁纸固定文件名。注意它不匹配 dateRe（8 位日期），
	// 所以 Cleanup 的日期淘汰不会碰它。
	customFile = "wallpaper.jpg"
)

// Meta 描述当前缓存的那张壁纸。
type Meta struct {
	Date      string    `json:"date"` // yyyymmdd
	Title     string    `json:"title"`
	File      string    `json:"file"` // 相对 dir 的文件名
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	Bytes     int64     `json:"bytes"`
	FetchedAt time.Time `json:"fetchedAt"`
}

type Manager struct {
	dir string
	// customRoot 下每个用户一个目录，自定义壁纸按用户隔离
	customRoot string
	client     *http.Client

	mu  sync.RWMutex
	cur *Meta
}

func NewManager(dir, customRoot string) *Manager {
	return &Manager{
		dir:        dir,
		customRoot: customRoot,
		client: &http.Client{
			Timeout: 25 * time.Second,
			Transport: &http.Transport{
				Proxy:               nil, // 壁纸走直连，不吃环境里的代理
				MaxIdleConnsPerHost: 2,
			},
		},
	}
}

// LoadCurrent 读取上次落盘的 meta；没有就返回 nil（不算错误）。
func (m *Manager) LoadCurrent() error {
	raw, err := os.ReadFile(filepath.Join(m.dir, metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("解析壁纸 meta: %w", err)
	}
	// meta 指向的文件可能已被清理
	if _, err := os.Stat(filepath.Join(m.dir, meta.File)); err != nil {
		return nil
	}
	m.mu.Lock()
	m.cur = &meta
	m.mu.Unlock()
	return nil
}

func (m *Manager) Current() (Meta, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cur == nil {
		return Meta{}, false
	}
	return *m.cur, true
}

func (m *Manager) FilePath(meta Meta) string {
	return filepath.Join(m.dir, meta.File)
}

// FetchDaily 拉取今天的 Bing 壁纸并落盘。已存在同一天的直接返回缓存。
func (m *Manager) FetchDaily(ctx context.Context) (Meta, error) {
	if cur, ok := m.Current(); ok && cur.Date == today() {
		if _, err := os.Stat(m.FilePath(cur)); err == nil {
			return cur, nil
		}
	}

	u, title, date, err := m.lookup(ctx)
	if err != nil {
		return Meta{}, err
	}

	body, err := m.download(ctx, u)
	if err != nil {
		return Meta{}, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return Meta{}, fmt.Errorf("下载内容不是合法图片: %w", err)
	}

	ext := ".jpg"
	if format == "png" {
		ext = ".png"
	}
	name := date + ext
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return Meta{}, err
	}
	if err := os.WriteFile(filepath.Join(m.dir, name), body, 0o644); err != nil {
		return Meta{}, err
	}
	// 有些环境的 umask 是 077，WriteFile 会被压成 600。
	// 数据目录可能被宿主机侧读取（备份/查看），显式校正一次。
	_ = os.Chmod(filepath.Join(m.dir, name), 0o644)

	meta := Meta{
		Date: date, Title: title, File: name,
		Width: cfg.Width, Height: cfg.Height, Bytes: int64(len(body)),
		FetchedAt: time.Now(),
	}
	if err := m.writeMeta(meta); err != nil {
		return Meta{}, err
	}
	m.mu.Lock()
	m.cur = &meta
	m.mu.Unlock()
	return meta, nil
}

type bingResp struct {
	Images []struct {
		StartDate string `json:"startdate"`
		Title     string `json:"title"`
		URL       string `json:"url"`
	} `json:"images"`
}

func (m *Manager) lookup(ctx context.Context) (imgURL, title, date string, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, bingAPI, nil)
	req.Header.Set("User-Agent", "justnav/0.1")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("请求 Bing 接口: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("Bing 接口返回 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", err
	}
	var br bingResp
	if err := json.Unmarshal(raw, &br); err != nil {
		return "", "", "", fmt.Errorf("解析 Bing 响应: %w", err)
	}
	if len(br.Images) == 0 {
		return "", "", "", errors.New("Bing 未返回图片")
	}
	im := br.Images[0]
	// 关键：UHD 变体。原 url 里带 _1920x1080，替换成 _UHD 才是真 4K。
	uhd := strings.Replace(im.URL, "_1920x1080", "_UHD", 1)
	if uhd == im.URL {
		uhd = im.URL // 万一格式变了，退回原图，不硬拼
	}
	if strings.HasPrefix(uhd, "/") {
		uhd = "https://www.bing.com" + uhd
	}
	return uhd, im.Title, im.StartDate, nil
}

func (m *Manager) download(ctx context.Context, u string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; justnav/0.1)")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载壁纸: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载壁纸返回 %d", resp.StatusCode)
	}
	// 多读 1 字节用于判断是否超限，避免把超大文件整个读进内存
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxDownload {
		return nil, fmt.Errorf("壁纸超过单张上限 %d MB", maxDownload>>20)
	}
	if len(body) < 1024 {
		return nil, errors.New("壁纸内容过小，疑似错误页")
	}
	return body, nil
}

func (m *Manager) writeMeta(meta Meta) error {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(m.dir, ".meta-*.tmp")
	f, err := os.CreateTemp(m.dir, filepath.Base(tmp))
	if err != nil {
		return err
	}
	name := f.Name()
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	f.Close()
	_ = os.Chmod(name, 0o644)
	return os.Rename(name, filepath.Join(m.dir, metaFile))
}

func today() string { return time.Now().Format("20060102") }

// ---------- 自定义上传壁纸 ----------
//
// 只接受 jpeg / png（stdlib 解不了 webp，不引第三方依赖）。
// 先 DecodeConfig 拿尺寸再决定要不要真解码 —— 一张 8000×6000 的图
// 解码后要 ~190MB 内存，NAS 上必须提前拦掉。

const maxCustomPixels = 24 << 20 // 2400 万像素（约 6000×4000）

// CustomPath 返回某个用户的自定义壁纸路径。
// 放在 <customRoot>/<userID>/wallpaper.jpg，与其他人完全隔离。
func (m *Manager) CustomPath(userID string) string {
	return filepath.Join(m.customRoot, userID, customFile)
}

func (m *Manager) HasCustom(userID string) bool {
	st, err := os.Stat(m.CustomPath(userID))
	return err == nil && st.Size() > 0
}

// SaveCustom 校验并落盘，返回最终尺寸。
func (m *Manager) SaveCustom(userID string, data []byte) (int, int, error) {
	// 文件类型真伪由 DecodeConfig 判定，这里只挡空包。
	// ⚠️ 不要用「体积太小」当判据：50×50 的纯色 PNG 只有几十字节，是合法图片。
	if len(data) < 8 {
		return 0, 0, errors.New("上传内容为空")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("不是可识别的图片: %w", err)
	}
	if format != "jpeg" && format != "png" {
		return 0, 0, fmt.Errorf("只支持 jpg / png，收到的是 %s", format)
	}
	if cfg.Width*cfg.Height > maxCustomPixels {
		return 0, 0, fmt.Errorf("图片太大（%d×%d），请压到 2400 万像素以内", cfg.Width, cfg.Height)
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("解码失败: %w", err)
	}

	// 统一重编码成 JPEG：PNG 截图动辄几十 MB，当背景没必要
	var buf bytes.Buffer
	buf.Grow(1 << 20)
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return 0, 0, fmt.Errorf("重编码失败: %w", err)
	}

	if userID == "" {
		return 0, 0, errors.New("缺少用户标识")
	}
	dir := filepath.Dir(m.CustomPath(userID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, 0, err
	}
	tmp, err := os.CreateTemp(dir, ".custom-*.tmp")
	if err != nil {
		return 0, 0, err
	}
	name := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(name)
		return 0, 0, err
	}
	tmp.Close()
	_ = os.Chmod(name, 0o644)
	// 原子替换：上传到一半断掉也不会留半张图
	if err := os.Rename(name, m.CustomPath(userID)); err != nil {
		os.Remove(name)
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}
