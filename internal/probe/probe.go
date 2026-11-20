// Package probe 定时探测每个导航条目的可达性。
//
// 判定口径（重要，别改成「只要 200 才算在线」）：
//
//	2xx / 3xx            → 在线
//	401 / 403 / 404      → 在线（服务活着，只是要登录或路径不同）
//	5xx                  → 离线（反代到不了上游 / 服务内部挂了）
//	连接失败 / 超时 / DNS → 离线
//
// 内网自签证书极常见，所以默认跳过 TLS 校验（可用配置关掉）。
//
// 抖动抑制：内网服务偶发超时很正常，单次失败就标红会让页面不停闪。
// 连续失败达到阈值（默认 2 次）才判离线，之前的状态标为 Pending。
package probe

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"
)

type Status struct {
	Online bool `json:"online"`
	// Pending 表示「本次没探通，但还没到判定阈值」。
	// 前端应按仍在线处理，但可以在 tooltip 里说明。
	Pending   bool      `json:"pending,omitempty"`
	Fails     int       `json:"fails,omitempty"`
	Threshold int       `json:"threshold,omitempty"`
	Code      int       `json:"code"`
	Ms        int64     `json:"ms"`
	At        time.Time `json:"at"`
	Err       string    `json:"err,omitempty"`
}

type Target struct {
	ID  string
	URL string
}

type Prober struct {
	mu     sync.RWMutex
	result map[string]Status
	fails  map[string]int

	client      *http.Client
	interval    time.Duration
	timeout     time.Duration
	concurrency int
	threshold   int
}

func New(interval, timeout time.Duration, insecure bool, threshold int) *Prober {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	if threshold < 1 {
		threshold = 1
	}
	tr := &http.Transport{
		Proxy: nil, // 内网地址不要走环境里的代理
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecure, // #nosec G402 —— 内网自签证书是常态
		},
		MaxIdleConnsPerHost: 2,
		DisableKeepAlives:   true,
	}
	return &Prober{
		result:      map[string]Status{},
		fails:       map[string]int{},
		client:      &http.Client{Transport: tr, Timeout: timeout},
		interval:    interval,
		timeout:     timeout,
		concurrency: 8,
		threshold:   threshold,
	}
}

func (p *Prober) Interval() time.Duration { return p.interval }
func (p *Prober) Threshold() int          { return p.threshold }

// All 返回当前状态的快照。
func (p *Prober) All() map[string]Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]Status, len(p.result))
	for k, v := range p.result {
		out[k] = v
	}
	return out
}

// Run 阻塞运行：启动先探一次，之后按 interval 轮询。
// 目标列表每次现取（条目可能被增删改），不在内部缓存。
func (p *Prober) Run(ctx context.Context, targets func() []Target) {
	p.Check(ctx, targets())

	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Check(ctx, targets())
		}
	}
}

// Check 并发探测一批目标。结果原地合并，已删除的条目会被清掉。
func (p *Prober) Check(ctx context.Context, list []Target) {
	sem := make(chan struct{}, p.concurrency)
	var wg sync.WaitGroup
	type pair struct {
		id string
		st Status
	}
	got := make([]pair, 0, len(list))
	var mu sync.Mutex

	for _, t := range list {
		if t.URL == "" || t.ID == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t Target) {
			defer wg.Done()
			defer func() { <-sem }()
			st := p.checkOne(ctx, t.URL)
			mu.Lock()
			got = append(got, pair{t.ID, st})
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()

	live := make(map[string]Status, len(got))
	nextFails := make(map[string]int, len(got))

	for _, g := range got {
		st := g.st
		st.Threshold = p.threshold
		if st.Online {
			nextFails[g.id] = 0
			live[g.id] = st
			continue
		}
		// 探失败：累计连续失败次数
		p.fails[g.id]++
		f := p.fails[g.id]
		st.Fails = f
		if f < p.threshold {
			// 还没到阈值，保持「在线」但标记 pending，避免页面抖动
			st.Pending = true
			st.Online = true
		}
		nextFails[g.id] = f
		live[g.id] = st
	}

	p.result = live
	// 清掉已经被删除的条目的计数
	for id := range p.fails {
		if _, ok := live[id]; !ok {
			delete(p.fails, id)
		}
	}
	for id, v := range nextFails {
		p.fails[id] = v
	}
}

func (p *Prober) checkOne(ctx context.Context, url string) Status {
	start := time.Now()

	// 先 HEAD：便宜。有些服务不支持 HEAD（405/501），退回 GET。
	code, err := p.request(ctx, http.MethodHead, url)
	if err == nil && (code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented) {
		code, err = p.request(ctx, http.MethodGet, url)
	}
	ms := time.Since(start).Milliseconds()

	st := Status{Ms: ms, At: time.Now()}
	switch {
	case err != nil:
		st.Online = false
		st.Err = shorten(err.Error())
	case code >= 500:
		st.Online = false
		st.Code = code
		st.Err = "服务端错误"
	default:
		// 2xx/3xx/401/403/404 都说明「有东西在应答」
		st.Online = true
		st.Code = code
	}
	return st
}

func (p *Prober) request(ctx context.Context, method, url string) (int, error) {
	c, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(c, method, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "justnav-probe/0.1")
	req.Header.Set("Accept", "*/*")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	// 必须把 body 读掉并关闭，否则连接泄漏
	_, _ = resp.Body.Read(make([]byte, 512))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func shorten(s string) string {
	if len(s) > 90 {
		return s[:90]
	}
	return s
}
