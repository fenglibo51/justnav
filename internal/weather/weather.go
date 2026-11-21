// Package weather 取当前天气。
//
// 数据源：Open-Meteo 为主，wttr.in 为备。
//
//	预报    https://api.open-meteo.com/v1/forecast
//	地理编码 https://geocoding-api.open-meteo.com/v1/search
//
// 2026-11-12 实测：api.open-meteo.com 从 NAS 直连不通（DNS 能解析出
// 51.81.167.44，但 TCP 握手就超时；旁路由代理也连不上），天气整块消失。
// 所以加了第二源 wttr.in（国内直连约 1 秒可通，返回 25KB，只取其中几个字段），
// 并且保留「上一次成功的数据」最多 6 小时 —— 上游抽风时挂旧数据，
// 比整个时间卡变成两行强。
//
// 对比过其他源：
//   - 中国天气网 d1.weather.com.cn  403，直接封
//   - 和风/高德                    都要 key
//
// 天气是「锦上添花」：两个源都拿不到就返回错误，前端隐藏那一块，绝不影响导航本身。
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	forecastURL = "https://api.open-meteo.com/v1/forecast"
	geocodeURL  = "https://geocoding-api.open-meteo.com/v1/search"
	wttrURL     = "https://wttr.in"
	// 天气变化不快，1 小时一次（用户定的）。实况温度在这段里最多变 2~3 度，
	// 出现在仪表盘上可以接受 —— 再长就会「傍晚还显示早上的温度」。
	// 跨天必须重取：高低温是「今天」的，昨天的拿到今天显示是错的，
	// 这个在 Current() 里按「同一天」判断。
	cacheTTL = time.Hour
	// 失败结果只记 1 分钟：1 分钟内不重复打上游，过了就再试（上游恢复得快）
	failTTL = time.Minute
	// 上次成功的数据最多撑 6 小时，超过就当没有
	staleTTL = 6 * time.Hour
	// 主源挂了一次就熔断 30 分钟：否则每 10 分钟缓存过期都要先白等 4 秒超时
	omBreak = 30 * time.Minute
)

type Now struct {
	City     string  `json:"city"`
	Temp     float64 `json:"temp"`
	Feels    float64 `json:"feels"`
	Humidity int     `json:"humidity"`
	Code     int     `json:"code"`
	Text     string  `json:"text"`
	Max      float64 `json:"max"`
	Min      float64 `json:"min"`
	// 这两个只有 TWC 给（它能见度单位 km、气压 hPa），别的源没有就留 0
	Visibility float64   `json:"visibility,omitempty"`
	Pressure   float64   `json:"pressure,omitempty"`
	At         time.Time `json:"at"`
}

type Place struct {
	Name      string  `json:"name"`
	Admin     string  `json:"admin"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
}

type cached struct {
	now  Now
	at   time.Time
	fail error
}

type Client struct {
	// 各源各用不同超时：一个源被墙的时候 8 秒全耗在它身上，备用源就没时间了
	om   *http.Client
	wttr *http.Client
	cn   *http.Client // 中国天气网：国内直连，不受代理影响
	twc  *http.Client // The Weather Channel（苹果天气同源）

	mu    sync.Mutex
	cache map[string]cached // key -> 10 分钟内的结果（成功或失败）
	last  map[string]Now    // key -> 最后一次成功，兜底用

	omDownUntil time.Time // 主源熔断到期时间

	cnCodes map[string]cnCodeEntry // 城市名 -> 中国天气网城市代码（30 分钟缓存）
}

// cnCodeEntry 城市代码缓存项
type cnCodeEntry struct {
	code string
	at   time.Time
}

func New() *Client {
	return &Client{
		om:      &http.Client{Timeout: 4 * time.Second},
		wttr:    &http.Client{Timeout: 6 * time.Second},
		cn:      &http.Client{Timeout: 5 * time.Second},
		twc:     &http.Client{Timeout: 6 * time.Second},
		cache:   map[string]cached{},
		last:    map[string]Now{},
		cnCodes: map[string]cnCodeEntry{},
	}
}

func (c *Client) Current(ctx context.Context, city string, lat, lon float64) (Now, error) {
	key := fmt.Sprintf("%.3f,%.3f", lat, lon)

	c.mu.Lock()
	if v, ok := c.cache[key]; ok {
		// 跨天就作废：高低温是「今天」的，昨天缓存下来的最高/最低拿到今天显示是错的。
		// 时间上可能只过了几十分钟（比如 23:50 拉的、00:10 再来问），但日期变了。
		nowT := time.Now()
		sameDay := v.at.Year() == nowT.Year() && v.at.YearDay() == nowT.YearDay()
		if v.fail == nil && sameDay && time.Since(v.at) < cacheTTL {
			c.mu.Unlock()
			return v.now, nil
		}
		if v.fail != nil && time.Since(v.at) < failTTL {
			// 刚失败过：别再打上游，直接走兜底
			stale, has := c.last[key]
			c.mu.Unlock()
			if has && time.Since(stale.At) < staleTTL {
				return stale, nil
			}
			return Now{}, v.fail
		}
	}
	c.mu.Unlock()

	now, err := c.fetch(ctx, city, lat, lon)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.cache[key] = cached{now: now, at: time.Now()}
		c.last[key] = now
		return now, nil
	}
	// 请求方自己断开（关页面/取消）不是上游的锅，别把这次失败记进缓存 ——
	// 否则一个提前关闭的页面会让整块天气在未来 1 分钟里都显示不出来
	if ctx.Err() != nil {
		return Now{}, err
	}
	c.cache[key] = cached{at: time.Now(), fail: err}
	if stale, ok := c.last[key]; ok && time.Since(stale.At) < staleTTL {
		return stale, nil // 上游全挂：挂上次的数据，总比空白强
	}
	return Now{}, err
}

type omResp struct {
	Current struct {
		Time        string  `json:"time"`
		Temp        float64 `json:"temperature_2m"`
		Feels       float64 `json:"apparent_temperature"`
		Humidity    int     `json:"relative_humidity_2m"`
		WeatherCode int     `json:"weather_code"`
	} `json:"current"`
	Daily struct {
		Code []int     `json:"weather_code"`
		Max  []float64 `json:"temperature_2m_max"`
		Min  []float64 `json:"temperature_2m_min"`
	} `json:"daily"`
}

// ============================================================
// 中国天气网（国内直连，主源）
// ------------------------------------------------------------
// 为什么加：2026-11-19 实测 open-meteo 和 wttr.in 从本机双双超时
// （DNS 解析到 Clash fake-ip 198.18.1.x 段，该段走不通），天气整块消失。
// 中国天气网走国内直连，实测 0.1 秒返回，免费、无需 API key。
// 换源只动后端：前端 wxIcon() 吃的是标准天气码，一行不用改。
// ============================================================

const (
	cnSearchURL  = "https://toy1.weather.com.cn/search"
	cnWeatherURL = "https://d1.weather.com.cn/weather_index/"
	cnUA         = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	cnReferer    = "https://www.weather.com.cn/"
)

// cnCode 城市名 → 中国天气网城市代码（如 宁波 → 101210401），30 分钟缓存
func (c *Client) cnCode(ctx context.Context, city string) (string, error) {
	if city == "" {
		return "", fmt.Errorf("城市为空")
	}
	c.mu.Lock()
	if v, ok := c.cnCodes[city]; ok && time.Since(v.at) < 30*time.Minute {
		c.mu.Unlock()
		return v.code, nil
	}
	c.mu.Unlock()

	q := url.Values{}
	q.Set("cityname", city)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cnSearchURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", cnUA)
	req.Header.Set("Referer", cnReferer)

	resp, err := c.cn.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("城市搜索返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	// 返回形如 ([{"ref":"101210401~zhejiang~宁波~Ningbo~..."},...])
	code := cnParseCode(body)
	if code == "" {
		return "", fmt.Errorf("没解析到城市代码")
	}
	c.mu.Lock()
	c.cnCodes[city] = cnCodeEntry{code: code, at: time.Now()}
	c.mu.Unlock()
	return code, nil
}

// cnParseCode 从搜索响应里抠第一个纯数字城市代码
func cnParseCode(body []byte) string {
	s := string(body)
	const key = `"ref":"`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	j := strings.Index(rest, "~")
	if j <= 0 {
		return ""
	}
	code := rest[:j]
	if len(code) < 9 || len(code) > 12 {
		return ""
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return code
}

// cnExtractVar 从 JS 里抠出 `var name ={...}` 的 JSON 对象（括号配平）
func cnExtractVar(s, name string) string {
	for _, pat := range []string{"var " + name + " =", "var " + name + "=", "var " + name + "	="} {
		i := strings.Index(s, pat)
		if i < 0 {
			continue
		}
		start := strings.IndexByte(s[i:], '{')
		if start < 0 {
			continue
		}
		start += i
		depth := 0
		for j := start; j < len(s); j++ {
			switch s[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return s[start : j+1]
				}
			}
		}
	}
	return ""
}

// cnTemp 解析中国天气网的温度字段，顺带挡掉「缺测」标记。
//
// 中国天气网用 999 表示缺测（实测宁波 cityDZ.temp 就是 999），
// 直接把 999 当温度显示会变成「999° / 6°」这种鬼东西，靠 >= 60 的范围校验挡住。
//
// ⚠️ 这里【不能】把 0 判成无效。原来写的是 `v <= -60 || v >= 60 || v == 0`，
//    那条 v == 0 直接导致 0°C 被当成缺测 —— 北京冬天 tempn="0" 是正常温度，
//    却被退回成「当前温度」，界面上就成了「-0.3° / -0.3°」这种没意义的高低温。
func cnTemp(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	if v <= -60 || v >= 60 {
		return 0, false
	}
	return v, true
}

// cnWMO 把中国天气网的天气描述转换成标准天气码（前端 wxIcon 用的那套）。
// 先按中文描述匹配 —— 描述比 d00/d01 这类编码更直白、更不容易错配。
func cnWMO(text, code string) int {
	has := func(ss ...string) bool {
		for _, s := range ss {
			if strings.Contains(text, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("雷"):
		return 95
	case has("雨夹雪", "冻雨"):
		return 68
	case has("阵雪"):
		return 85
	case has("暴雪", "大雪"):
		return 75
	case has("中雪"):
		return 73
	case has("小雪", "雪"):
		return 71
	case has("阵雨"):
		return 80
	case has("暴雨", "大雨", "大暴雨", "特大暴雨"):
		return 65
	case has("中雨"):
		return 63
	case has("小雨", "雨"):
		return 61
	case has("雾", "霾", "沙", "尘"):
		return 45
	case has("阴"):
		return 3
	case has("多云"):
		return 2
	case has("晴"):
		return 0
	}
	// 描述认不出时退回编码（d00 晴 / d01 多云 / d02 阴 / d03 阵雨 / d04 雷阵雨）
	switch {
	case strings.HasSuffix(code, "00"):
		return 0
	case strings.HasSuffix(code, "01"):
		return 2
	case strings.HasSuffix(code, "02"):
		return 3
	}
	return 3
}

// fetchCN 中国天气网主源：实况取 dataSK，今明温度取 cityDZ
func (c *Client) fetchCN(ctx context.Context, city string) (Now, error) {
	code, err := c.cnCode(ctx, city)
	if err != nil {
		return Now{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cnWeatherURL+code+".html", nil)
	if err != nil {
		return Now{}, err
	}
	req.Header.Set("User-Agent", cnUA)
	req.Header.Set("Referer", cnReferer)

	resp, err := c.cn.Do(req)
	if err != nil {
		return Now{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Now{}, fmt.Errorf("天气页返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return Now{}, err
	}
	raw := string(body)

	// 实况
	var sk struct {
		City        string `json:"cityname"`
		Temp        string `json:"temp"`
		SD          string `json:"SD"`
		Weather     string `json:"weather"`
		WeatherCode string `json:"weathercode"`
		WD          string `json:"WD"`
		WS          string `json:"WS"`
		Time        string `json:"time"`
	}
	skJSON := cnExtractVar(raw, "dataSK")
	if skJSON == "" {
		return Now{}, fmt.Errorf("没有实况数据")
	}
	if err := json.Unmarshal([]byte(skJSON), &sk); err != nil {
		return Now{}, fmt.Errorf("实况解析失败: %w", err)
	}

	// 今日预报（最高/最低）
	var dz struct {
		WeatherInfo struct {
			City        string `json:"city"`
			Temp        string `json:"temp"`
			TempN       string `json:"tempn"`
			Weather     string `json:"weather"`
			WeatherCode string `json:"weathercode"`
		} `json:"weatherinfo"`
	}
	if dzJSON := cnExtractVar(raw, "cityDZ"); dzJSON != "" {
		_ = json.Unmarshal([]byte(dzJSON), &dz) // 预报解析失败不算致命，实况有就够
	}

	temp, err := strconv.ParseFloat(sk.Temp, 64)
	if err != nil {
		return Now{}, fmt.Errorf("温度格式异常: %q", sk.Temp)
	}

	now := Now{
		City: sk.City,
		Temp: temp,
		Code: cnWMO(sk.Weather, sk.WeatherCode),
		Text: sk.Weather,
		At:   time.Now(),
	}
	if now.City == "" {
		now.City = city
	}
	// 湿度 "39%" → 39
	if h := strings.TrimSuffix(sk.SD, "%"); h != "" {
		if v, e := strconv.Atoi(h); e == nil {
			now.Humidity = v
		}
	}
	// 今明温度：cityDZ 存在就用它，否则实况温度顶上，避免前端显示 "° / °"
	// ⚠️ 中国天气网用 999 表示「缺测」，不是真实温度 —— 2026-11-19 实测
	//    宁波 cityDZ.temp = "999"，前端直接显示成「999° / 6°」。
	//    所以这里必须做一次物理范围校验，超出就退回实况温度。
	if v, ok := cnTemp(dz.WeatherInfo.Temp); ok {
		now.Max = v
	} else {
		now.Max = temp
	}
	if v, ok := cnTemp(dz.WeatherInfo.TempN); ok {
		now.Min = v
	} else {
		now.Min = temp
	}
	// 实况描述缺失时用今日预报的描述补
	if now.Text == "" && dz.WeatherInfo.Weather != "" {
		now.Text = dz.WeatherInfo.Weather
		now.Code = cnWMO(dz.WeatherInfo.Weather, dz.WeatherInfo.WeatherCode)
	}
	return now, nil
}

// fetch 按 TWC → 中国天气网 → open-meteo → wttr.in 的顺序问，第一个成功的就用。
// TWC 排第一是因为它的数据最全（自带真实当日高低），中国天气网的高低温常年 999。
func (c *Client) fetch(ctx context.Context, city string, lat, lon float64) (Now, error) {
	// ① The Weather Channel（苹果天气同源）：一次请求就带全实况 + 真实当日高低，
	//    国内可直连。放第一位是因为下面中国天气网的高低温经常是 999（缺测）。
	nowTWC, errTWC := c.fetchTWC(ctx, city, lat, lon)
	if errTWC == nil {
		return nowTWC, nil
	}

	// ② 中国天气网：本地源，实况中文更地道，但 cityDZ 的最高温经常缺测
	nowCN, errCN := c.fetchCN(ctx, city)
	if errCN == nil {
		// ★ 中国天气网经常把「当天最高温」给成 999（缺测，夜间尤其常见），fetchCN 会退回
		//   成当前温度，于是出现 max <= min —— 实测宁波返回过 {"temp":4.4,"max":4.4,"min":7}
		//   这种「最高温比最低温还低」的矛盾数据。
		//
		//   补问一次 open-meteo 拿真实日高低，但【必须加超时】：这个源在国内网络下经常
		//   直接连不上，实测不设限会把一次 /api/weather 拖到 4.5 秒（正常 0.5 秒）。
		//   1.5 秒拿不到就算了 —— 前端在 max <= min 时本来就不显示高低温。
		//   失败后同样走 omDownUntil 熔断，30 分钟内不再试。
		c.mu.Lock()
		skipOM := time.Now().Before(c.omDownUntil)
		c.mu.Unlock()

		if nowCN.Max <= nowCN.Min && lat != 0 && lon != 0 && !skipOM {
			omCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			om, err := c.fetchOpenMeteo(omCtx, city, lat, lon)
			cancel()
			if err == nil && om.Max > om.Min {
				nowCN.Max, nowCN.Min = om.Max, om.Min
			} else {
				c.mu.Lock()
				c.omDownUntil = time.Now().Add(omBreak)
				c.mu.Unlock()
			}
		}
		return nowCN, nil
	}

	c.mu.Lock()
	skipOM := time.Now().Before(c.omDownUntil)
	c.mu.Unlock()

	var errOM error
	if skipOM {
		errOM = fmt.Errorf("主源熔断中")
	} else {
		now, err := c.fetchOpenMeteo(ctx, city, lat, lon)
		if err == nil {
			return now, nil
		}
		errOM = err
		c.mu.Lock()
		c.omDownUntil = time.Now().Add(omBreak)
		c.mu.Unlock()
	}

	now, errW := c.fetchWttr(ctx, city, lat, lon)
	if errW == nil {
		return now, nil
	}
	return Now{}, fmt.Errorf("天气源都失败：TWC(%v) / 中国天气网(%v) / open-meteo(%v) / wttr(%v)", errTWC, errCN, errOM, errW)
}

func (c *Client) fetchOpenMeteo(ctx context.Context, city string, lat, lon float64) (Now, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(lon, 'f', 4, 64))
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,weather_code")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min")
	q.Set("timezone", "auto")
	q.Set("forecast_days", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, forecastURL+"?"+q.Encode(), nil)
	if err != nil {
		return Now{}, err
	}
	req.Header.Set("User-Agent", "justnav/0.2")

	resp, err := c.om.Do(req)
	if err != nil {
		return Now{}, fmt.Errorf("请求天气服务: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Now{}, fmt.Errorf("天气服务返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Now{}, err
	}
	var r omResp
	if err := json.Unmarshal(body, &r); err != nil {
		return Now{}, fmt.Errorf("解析天气: %w", err)
	}

	now := Now{
		City:     city,
		Temp:     r.Current.Temp,
		Feels:    r.Current.Feels,
		Humidity: r.Current.Humidity,
		Code:     r.Current.WeatherCode,
		Text:     CodeText(r.Current.WeatherCode),
		At:       time.Now(),
	}
	if len(r.Daily.Max) > 0 {
		now.Max = r.Daily.Max[0]
	}
	if len(r.Daily.Min) > 0 {
		now.Min = r.Daily.Min[0]
	}
	return now, nil
}

// Geocode 城市名 → 经纬度（支持中文）。
func (c *Client) Geocode(ctx context.Context, name string) (Place, error) {
	if name == "" {
		return Place{}, fmt.Errorf("城市名不能为空")
	}
	p, errOM := c.geocodeOpenMeteo(ctx, name)
	if errOM == nil {
		return p, nil
	}
	p, errW := c.geocodeWttr(ctx, name)
	if errW == nil {
		return p, nil
	}
	return Place{}, fmt.Errorf("城市查询失败：open-meteo(%v) / wttr(%v)", errOM, errW)
}

func (c *Client) geocodeOpenMeteo(ctx context.Context, name string) (Place, error) {
	q := url.Values{}
	q.Set("name", name)
	q.Set("count", "1")
	q.Set("language", "zh")
	q.Set("format", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geocodeURL+"?"+q.Encode(), nil)
	if err != nil {
		return Place{}, err
	}
	req.Header.Set("User-Agent", "justnav/0.2")

	resp, err := c.om.Do(req)
	if err != nil {
		return Place{}, fmt.Errorf("请求地理编码: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Place{}, fmt.Errorf("地理编码返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Place{}, err
	}
	var r struct {
		Results []struct {
			Name      string  `json:"name"`
			Admin1    string  `json:"admin1"`
			Country   string  `json:"country"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Timezone  string  `json:"timezone"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Place{}, fmt.Errorf("解析地理编码: %w", err)
	}
	if len(r.Results) == 0 {
		return Place{}, fmt.Errorf("找不到城市「%s」", name)
	}
	x := r.Results[0]
	return Place{
		Name: x.Name, Admin: x.Admin1, Latitude: x.Latitude,
		Longitude: x.Longitude, Timezone: x.Timezone,
	}, nil
}

// fetchWttr 备用源。wttr.in 一次 25KB、字段全是字符串，只取需要的几个。
func (c *Client) fetchWttr(ctx context.Context, city string, lat, lon float64) (Now, error) {
	u := fmt.Sprintf("%s/%s?format=j1", wttrURL, url.PathEscape(fmt.Sprintf("%.4f,%.4f", lat, lon)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Now{}, err
	}
	req.Header.Set("User-Agent", "justnav/0.2")

	resp, err := c.wttr.Do(req)
	if err != nil {
		return Now{}, fmt.Errorf("请求 wttr: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Now{}, fmt.Errorf("wttr 返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return Now{}, err
	}
	var r struct {
		Current []struct {
			TempC    string `json:"temp_C"`
			FeelsC   string `json:"FeelsLikeC"`
			Humidity string `json:"humidity"`
			Code     string `json:"weatherCode"`
		} `json:"current_condition"`
		Weather []struct {
			MaxC string `json:"maxtempC"`
			MinC string `json:"mintempC"`
		} `json:"weather"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Now{}, fmt.Errorf("解析 wttr: %w", err)
	}
	if len(r.Current) == 0 {
		return Now{}, fmt.Errorf("wttr 没有当前天气字段")
	}
	cur := r.Current[0]
	code := wwoToWMO(int(num(cur.Code)))
	now := Now{
		City:     city,
		Temp:     num(cur.TempC),
		Feels:    num(cur.FeelsC),
		Humidity: int(num(cur.Humidity)),
		Code:     code,
		Text:     CodeText(code),
		At:       time.Now(),
	}
	if len(r.Weather) > 0 {
		now.Max = num(r.Weather[0].MaxC)
		now.Min = num(r.Weather[0].MinC)
	}
	return now, nil
}

// geocodeWttr 备用的城市反查：wttr 的 nearest_area 直接给英文名 + 经纬度。
// 中文名保留用户输入的那一个，界面上还是「宁波」，不是「Ningbo」。
func (c *Client) geocodeWttr(ctx context.Context, name string) (Place, error) {
	u := fmt.Sprintf("%s/%s?format=j1", wttrURL, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Place{}, err
	}
	req.Header.Set("User-Agent", "justnav/0.2")

	resp, err := c.wttr.Do(req)
	if err != nil {
		return Place{}, fmt.Errorf("请求 wttr 地理编码: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Place{}, fmt.Errorf("wttr 地理编码返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return Place{}, err
	}
	var r struct {
		Nearest []struct {
			AreaName []struct {
				Value string `json:"value"`
			} `json:"areaName"`
			Region []struct {
				Value string `json:"value"`
			} `json:"region"`
			Lat string `json:"latitude"`
			Lon string `json:"longitude"`
		} `json:"nearest_area"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Place{}, fmt.Errorf("解析 wttr 地理编码: %w", err)
	}
	if len(r.Nearest) == 0 {
		return Place{}, fmt.Errorf("wttr 找不到城市「%s」", name)
	}
	n := r.Nearest[0]
	lat, lon := num(n.Lat), num(n.Lon)
	if lat == 0 && lon == 0 {
		return Place{}, fmt.Errorf("wttr 找不到城市「%s」", name)
	}
	admin := ""
	if len(n.Region) > 0 {
		admin = n.Region[0].Value
	}
	return Place{Name: name, Admin: admin, Latitude: lat, Longitude: lon}, nil
}

// wwoToWMO 把 wttr 用的 WWO 天气代码翻成 WMO 代码。
// 前端图标和 CodeText 都按 WMO 走，这里转一次，两边不用各留一套判断。
func wwoToWMO(c int) int {
	switch c {
	case 113:
		return 0 // 晴
	case 116:
		return 2 // 多云
	case 119, 122:
		return 3 // 阴 / 阴天
	case 143, 145, 148, 149, 248:
		return 45 // 雾 / 霾
	case 260:
		return 48 // 冻雾
	case 176, 293, 296, 353:
		return 61 // 小雨
	case 263, 266:
		return 51 // 毛毛雨
	case 299, 302, 356:
		return 63 // 中雨
	case 305, 308, 359:
		return 65 // 大雨
	case 185, 281:
		return 56 // 冻毛毛雨
	case 284:
		return 57
	case 182, 311, 317, 362:
		return 66 // 冻雨 / 雨夹雪
	case 314, 320, 365:
		return 67
	case 179, 227, 323, 326, 368:
		return 71 // 小雪
	case 329, 332:
		return 73 // 中雪
	case 230, 335, 338, 371:
		return 75 // 大雪
	case 350, 374, 377:
		return 77 // 雪粒
	case 386, 389, 392, 395:
		return 95 // 雷阵雨
	}
	return 3 // 认不出来就按阴天显示，别显示「未知」
}

// num 解析 wttr 的字符串数字，解析不了就是 0（缺字段时不该整块报错）
func num(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

// CodeText 把 WMO 天气代码翻译成中文。前端图标也用同一套代码。
func CodeText(code int) string {
	switch code {
	case 0:
		return "晴"
	case 1:
		return "晴间多云"
	case 2:
		return "多云"
	case 3:
		return "阴"
	case 45, 48:
		return "雾"
	case 51, 53, 55:
		return "毛毛雨"
	case 56, 57:
		return "冻毛毛雨"
	case 61:
		return "小雨"
	case 63:
		return "中雨"
	case 65:
		return "大雨"
	case 66, 67:
		return "冻雨"
	case 71:
		return "小雪"
	case 73:
		return "中雪"
	case 75:
		return "大雪"
	case 77:
		return "雪粒"
	case 80, 81:
		return "阵雨"
	case 82:
		return "强阵雨"
	case 85, 86:
		return "阵雪"
	case 95:
		return "雷阵雨"
	case 96, 99:
		return "雷暴伴冰雹"
	default:
		return "未知"
	}
}
