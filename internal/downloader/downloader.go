// Package downloader 查 qBittorrent / Transmission 的实时状态。
//
// 只读：qb 用 /api/v2/transfer/info + /api/v2/torrents/info，tr 用
// session-stats + torrent-get，全是查询，不动任何任务。
//
// 认证：两家都不给匿名（实测 qb 403 / tr 401，连 NAS 本机访问也一样），
// 所以必须带账号密码 —— 跟 homepage 那些导航页的做法一致。
// 凭据只在服务端用，永不回前端。
package downloader

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"

	"justnav/internal/store"
	"sync"
)

// Stats 一台下载器的实时状态。OK=false 时看 Err。
// Task 是卡片上要展示的「正在跑的那几条」，只取最关键的三样：
// 名字、进度、当前速度。完整任务列表太长，卡片放不下也用不着。
type Task struct {
	Name     string  `json:"name"`
	Progress float64 `json:"progress"`        // 0..1
	Speed    int64   `json:"speed,omitempty"` // bytes/s
	State    string  `json:"state"`           // down | seed
}

type Stats struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type"`
	URL      string `json:"url,omitempty"`
	OK       bool   `json:"ok"`
	Err      string `json:"err,omitempty"`
	Version  string `json:"version,omitempty"`  // qb: app/version；tr: rpc-version
	DLSpeed  int64  `json:"dlSpeed"`            // bytes/s
	UPSpeed  int64  `json:"upSpeed"`            // bytes/s
	Download int    `json:"download"`           // 正在下载的任务数
	Seed     int    `json:"seed"`               // 做种中的任务数
	Paused   int    `json:"paused"`             // 暂停/停止的任务数
	Total    int    `json:"total"`              // 任务总数
	Free     int64  `json:"free,omitempty"`     // 剩余空间（bytes，能拿到就给）
	AllDL    int64  `json:"allDl,omitempty"`    // 累计下载量（bytes）
	AllUL    int64  `json:"allUl,omitempty"`    // 累计上传量（bytes）
	Tasks    []Task `json:"tasks,omitempty"`    // 活跃任务，最多 2 条
}

var httpClient = &http.Client{
	Timeout: 8 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, // 内网自签证书
		MaxIdleConnsPerHost: 2,
		DisableKeepAlives:   true,
	},
}

// Fetch 拉一台下载器的状态。出错不返回 error，错误塞进 Stats.Err —— 卡片上一台挂了
// 不该影响另一台显示。
// ---------- 认证失败熔断（2026-11-20 血的教训）----------
//
// qBittorrent 默认 WebUI\MaxAuthenticationFailCount=5 + BanDuration=3600：
// 同一个 IP 认证失败 5 次就封一小时。卡片每 15 秒轮询一次，
// 密码填错 75 秒就能把人自己的 IP 封死 —— 实测已经把某台内网机的 IP 封了一小时。
// 所以这里做熔断：同一台认证失败 2 次，冷却 20 分钟内直接返回上次的错，
// 一个字节都不往那边发。用户改完配置点「检测连接」走 FetchForce，绕过熔断。

const (
	authFailLimit = 2
	authFailCool  = 20 * time.Minute
)

var (
	tripMu  sync.Mutex
	tripBox = map[string]*tripState{}
)

type tripState struct {
	fails int
	until time.Time
	err   string
}

func tripKey(c store.Downloader) string {
	return strings.ToLower(c.Type) + "|" + strings.TrimSpace(c.URL)
}

// isAuthFail：这条错是不是「密码/认证不对」。这类重试只会加重封禁；
// 网络不通那种重试无妨，不该触发熔断。
func isAuthFail(msg string) bool {
	for _, k := range []string{"账号或密码不对", "被拒绝（403）", "401", "封禁", "banned"} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

func tripCheck(c store.Downloader) (string, bool) {
	tripMu.Lock()
	defer tripMu.Unlock()
	k := tripKey(c)
	t := tripBox[k]
	if t == nil || t.fails < authFailLimit {
		return "", false
	}
	if time.Now().After(t.until) {
		delete(tripBox, k)
		return "", false
	}
	left := int(time.Until(t.until).Minutes()) + 1
	return t.err + fmt.Sprintf("｜已连错 %d 次，%d 分钟内不再自动重试（防止被下载器封 IP）", t.fails, left), true
}

func tripResult(c store.Downloader, errMsg string) {
	tripMu.Lock()
	defer tripMu.Unlock()
	k := tripKey(c)
	if errMsg == "" {
		delete(tripBox, k) // 成功一次就把账一笔勾销
		return
	}
	if !isAuthFail(errMsg) {
		return
	}
	t := tripBox[k]
	if t == nil {
		t = &tripState{}
		tripBox[k] = t
	}
	t.fails++
	t.err = errMsg
	t.until = time.Now().Add(authFailCool)
}

// Fetch 自动轮询用（带熔断）。
func Fetch(c store.Downloader) Stats { return fetch(c, false) }

// FetchForce 手动「检测连接」用：跳过熔断，用户刚改完配置得能立刻试。
func FetchForce(c store.Downloader) Stats { return fetch(c, true) }

func fetch(c store.Downloader, force bool) Stats {
	st := Stats{ID: c.ID, Name: c.Name, Type: c.Type, URL: c.URL}
	if c.Name == "" {
		st.Name = c.Type
	}
	if !force {
		if msg, hit := tripCheck(c); hit {
			st.OK = false
			st.Err = msg
			return st
		}
	}
	var err error
	switch strings.ToLower(c.Type) {
	case "qb", "qbittorrent":
		err = fetchQB(c, &st)
	case "tr", "transmission":
		err = fetchTR(c, &st)
	default:
		err = fmt.Errorf("不认识的类型 %q（只支持 qb / tr）", c.Type)
	}
	if err != nil {
		st.OK = false
		st.Err = friendlyErr(err)
	}
	tripResult(c, st.Err)
	return st
}

// friendlyErr 把 Go 的 net 原始报错翻成人话。
// 直接甩 "dial tcp 10.0.0.9:8085: connect: no route to host" 出去，
// 卡片上根本看不下去（而且会撑成好几行把布局顶歪）。
func friendlyErr(err error) string {
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "no route to host"):
		return "连不上，网络不通（IP 或网段不对）"
	case strings.Contains(low, "connection refused"):
		return "连不上，端口没开（服务没启动？）"
	case strings.Contains(low, "i/o timeout"), strings.Contains(low, "deadline exceeded"),
		strings.Contains(low, "context deadline"):
		return "连不上，超时（防火墙？地址填错？）"
	case strings.Contains(low, "no such host"):
		return "域名解析不了"
	case strings.Contains(low, "certificate"), strings.Contains(low, "tls"):
		return "TLS 握手失败（自签证书？试试 http://）"
	case strings.Contains(msg, "账号或密码不对"):
		return msg
	}
	// 兜底：截断，别让一行占满屏幕
	if len([]rune(msg)) > 46 {
		return string([]rune(msg)[:46]) + "…"
	}
	return msg
}

// ───────────────────────── qBittorrent ─────────────────────────

func qbBase(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", errors.New("没填地址")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "http://" + u
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" {
		return "", fmt.Errorf("地址不对：%s", raw)
	}
	pu.Path = strings.TrimSuffix(pu.Path, "/")
	return pu.String(), nil
}

func fetchQB(c store.Downloader, st *Stats) error {
	base, err := qbBase(c.URL)
	if err != nil {
		return err
	}
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{
		Timeout: 8 * time.Second,
		Jar:     jar,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true,
		},
		// 不跟随重定向：qb 登录失败会 302 到别处，跟过去会让报错变笼统
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	// 1) 登录
	form := url.Values{}
	form.Set("username", c.User)
	form.Set("password", c.Pass)
	req, _ := http.NewRequest("POST", base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", base)
	req.Header.Set("User-Agent", "justnav/1")
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("连不上：%v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode == 403 {
		return errors.New("被拒绝（403）—— qb 的「对本地地址跳过认证」选项没开，且账号密码不对")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("登录失败 HTTP %d", resp.StatusCode)
	}
	if !strings.Contains(strings.ToLower(string(body)), "ok") {
		return errors.New("账号或密码不对")
	}

	// 2) 版本
	if v, err := qbGet(cl, base+"/api/v2/app/version"); err == nil {
		st.Version = strings.TrimSpace(string(v))
	}

	// 3) 速率
	tb, err := qbGet(cl, base+"/api/v2/transfer/info")
	if err != nil {
		return fmt.Errorf("读速率失败：%v", err)
	}
	var ti struct {
		DLSpeed int64 `json:"dl_info_speed"`
		UPSpeed int64 `json:"up_info_speed"`
		DLData  int64 `json:"dl_info_data"`
		UPData  int64 `json:"up_info_data"`
	}
	if err := json.Unmarshal(tb, &ti); err != nil {
		return fmt.Errorf("速率返回看不懂：%v", err)
	}
	st.DLSpeed, st.UPSpeed = ti.DLSpeed, ti.UPSpeed
	st.AllDL, st.AllUL = ti.DLData, ti.UPData

	// 累计量和磁盘剩多少只有 sync/maindata 有。拿不到不算致命（老版本 qb 或权限问题），
	// 上面 transfer/info 给的本次会话累计已经够垫底了。
	if sb, err := qbGet(cl, base+"/api/v2/sync/maindata"); err == nil {
		var md struct {
			ServerState struct {
				FreeSpace int64 `json:"free_space_on_disk"`
				AllTimeDL int64 `json:"alltime_dl"`
				AllTimeUL int64 `json:"alltime_ul"`
			} `json:"server_state"`
		}
		if json.Unmarshal(sb, &md) == nil {
			ss := md.ServerState
			if ss.FreeSpace > 0 {
				st.Free = ss.FreeSpace
			}
			if ss.AllTimeDL > 0 {
				st.AllDL = ss.AllTimeDL
			}
			if ss.AllTimeUL > 0 {
				st.AllUL = ss.AllTimeUL
			}
		}
	}

	// 4) 任务
	lb, err := qbGet(cl, base+"/api/v2/torrents/info")
	if err != nil {
		return fmt.Errorf("读任务失败：%v", err)
	}
	var list []struct {
		State    string  `json:"state"`
		Name     string  `json:"name"`
		Progress float64 `json:"progress"`
		DLSpeed  int64   `json:"dlspeed"`
		UPSpeed  int64   `json:"upspeed"`
	}
	if err := json.Unmarshal(lb, &list); err != nil {
		return fmt.Errorf("任务返回看不懂：%v", err)
	}
	st.Total = len(list)
	var downs, seeds []Task
	for _, t := range list {
		switch t.State {
		case "downloading", "metaDL", "forcedDL", "stalledDL", "queuedDL",
			"allocating", "checkingDL", "checkingResumeData", "moving":
			st.Download++
			downs = append(downs, Task{Name: t.Name, Progress: t.Progress, Speed: t.DLSpeed, State: "down"})
		case "uploading", "forcedUP", "stalledUP", "queuedUP", "checkingUP":
			st.Seed++
			seeds = append(seeds, Task{Name: t.Name, Progress: t.Progress, Speed: t.UPSpeed, State: "seed"})
		default:
			st.Paused++
		}
	}
	st.Tasks = pickTasks(downs, seeds)
	st.OK = true
	return nil
}

// pickTasks 挑卡片上显示的两条任务：正在下载的优先（按速度从快到慢），
// 下载中的不足两条时用做种中的补齐。都按速度排，因为卡片上最想看的就是「谁在跑」。
func pickTasks(downs, seeds []Task) []Task {
	sortTasks := func(ts []Task) {
		sort.SliceStable(ts, func(i, j int) bool { return ts[i].Speed > ts[j].Speed })
	}
	sortTasks(downs)
	sortTasks(seeds)
	out := make([]Task, 0, 2)
	seen := map[string]bool{}
	push := func(ts []Task) {
		for _, t := range ts {
			if len(out) >= 2 {
				return
			}
			// 同名任务只留一条：家里几块盘挂同一个种子时 qb 会列出好几条一样的，
			// 卡片上重复两行看着像 bug。
			if seen[t.Name] {
				continue
			}
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	push(downs)
	push(seeds)
	return out
}

func qbGet(cl *http.Client, u string) ([]byte, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Referer", u)
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == 403 {
		return nil, errors.New("403（cookie 没带上）")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return b, nil
}

// ───────────────────────── Transmission ─────────────────────────

func trBase(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", errors.New("没填地址")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "http://" + u
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" {
		return "", fmt.Errorf("地址不对：%s", raw)
	}
	// 填了 9091 就补 /transmission/rpc；已经带 rpc 路径就用原样
	if !strings.Contains(pu.Path, "/rpc") {
		pu.Path = "/transmission/rpc"
	}
	return pu.String(), nil
}

func trCall(cl *http.Client, base, sessionID string, method string, args any, user, pass string) (json.RawMessage, string, error) {
	payload := map[string]any{"method": method}
	if args != nil {
		payload["arguments"] = args
	}
	buf, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", base, strings.NewReader(string(buf)))
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("X-Transmission-Session-Id", sessionID)
	}
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, sessionID, fmt.Errorf("连不上：%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	if resp.StatusCode == 401 {
		return nil, sessionID, errors.New("账号或密码不对（401）")
	}
	if resp.StatusCode == 409 {
		// 首次访问必然 409，回一个 session id，带上去重放
		sid := resp.Header.Get("X-Transmission-Session-Id")
		if sid == "" {
			return nil, sessionID, errors.New("服务端没给 session id")
		}
		return nil, sid, errNeedRetry
	}
	if resp.StatusCode != 200 {
		return nil, sessionID, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var wrap struct {
		Result    string          `json:"result"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, sessionID, fmt.Errorf("返回不是 JSON：%v", err)
	}
	if wrap.Result != "success" {
		return nil, sessionID, fmt.Errorf("RPC 报错：%s", wrap.Result)
	}
	return wrap.Arguments, sessionID, nil
}

var errNeedRetry = errors.New("need-retry")

func fetchTR(c store.Downloader, st *Stats) error {
	base, err := trBase(c.URL)
	if err != nil {
		return err
	}
	cl := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true,
		},
	}

	// 先用 session-stats 探路（首次 409 拿 session id，然后再来一遍）
	args, sid, err := trCall(cl, base, "", "session-stats", nil, c.User, c.Pass)
	if err == errNeedRetry {
		args, sid, err = trCall(cl, base, sid, "session-stats", nil, c.User, c.Pass)
	}
	if err != nil {
		return err
	}
	var ss struct {
		DLSpeed     int64 `json:"downloadSpeed"`
		UPSpeed     int64 `json:"uploadSpeed"`
		ActiveCount int   `json:"activeTorrentCount"`
		Count       int   `json:"torrentCount"`
		Cumulative  struct {
			Downloaded int64 `json:"downloadedBytes"`
			Uploaded   int64 `json:"uploadedBytes"`
		} `json:"cumulative-stats"`
	}
	if err := json.Unmarshal(args, &ss); err != nil {
		return fmt.Errorf("session-stats 看不懂：%v", err)
	}
	st.DLSpeed, st.UPSpeed, st.Total = ss.DLSpeed, ss.UPSpeed, ss.Count
	st.AllDL, st.AllUL = ss.Cumulative.Downloaded, ss.Cumulative.Uploaded

	// 磁盘剩余单独问一次；tr 老版本可能没这个 RPC，失败就当没拿到
	if fa, _, e2 := trCall(cl, base, sid, "free-space", nil, c.User, c.Pass); e2 == nil {
		var fs struct {
			SizeBytes int64 `json:"size-bytes"`
		}
		if json.Unmarshal(fa, &fs) == nil && fs.SizeBytes > 0 {
			st.Free = fs.SizeBytes
		}
	}

	// 再拿任务明细统计各状态
	fields := []string{"status", "name", "percentDone", "rateDownload", "rateUpload"}
	ta, _, err := trCall(cl, base, sid, "torrent-get",
		map[string]any{"fields": fields}, c.User, c.Pass)
	if err == errNeedRetry {
		ta, _, err = trCall(cl, base, sid, "torrent-get",
			map[string]any{"fields": fields}, c.User, c.Pass)
	}
	if err != nil {
		// 任务明细拿不到不算致命：速率和总数已经有了
		st.OK = true
		return nil
	}
	var wrap struct {
		Torrents []struct {
			Status   int     `json:"status"`
			Name     string  `json:"name"`
			Percent  float64 `json:"percentDone"`
			RateDown int64   `json:"rateDownload"`
			RateUp   int64   `json:"rateUpload"`
		} `json:"torrents"`
	}
	if err := json.Unmarshal(ta, &wrap); err != nil {
		st.OK = true
		return nil
	}
	var downs, seeds []Task
	for _, t := range wrap.Torrents {
		switch t.Status {
		case 3, 4: // queued to download / downloading
			st.Download++
			downs = append(downs, Task{Name: t.Name, Progress: t.Percent, Speed: t.RateDown, State: "down"})
		case 5, 6: // queued to seed / seeding
			st.Seed++
			seeds = append(seeds, Task{Name: t.Name, Progress: t.Percent, Speed: t.RateUp, State: "seed"})
		default: // 0 停止 / 1 待检 / 2 校验
			st.Paused++
		}
	}
	st.Tasks = pickTasks(downs, seeds)
	st.OK = true
	return nil
}

// Test 检测连接，就是跑一遍 Fetch 然后把结果原样返回 —— 卡片和弹窗用同一套逻辑，
// 免得「检测成功但卡片显示失败」这种两套代码的说法。
// Test 手动「检测连接」：绕过熔断（用户刚改完配置必须能立刻试）。
func Test(c store.Downloader) Stats { return FetchForce(c) }
